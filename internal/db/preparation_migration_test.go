package db

import (
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenMigratesPreviewPreparationAndPreservesNotificationAcks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		preview bool
		version uint
	}{
		{"preview-v54", true, 54},
		{"preview-v59", true, 59},
		{"released-v59", false, 59},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			assert := assert.New(t)
			dbPath := filepath.Join(t.TempDir(), "preparation.db")
			previewNames := strings.NewReplacer(
				"forge_spoke_preparation", "forge_node_preparation",
				"hub_node_id", "coordinator_node_id",
				"hub_receipt", "coordinator_receipt",
			)

			// Build the historical shape directly, then apply the later shipped
			// migrations normally, including v56's trigger-only repair.
			files := fstest.MapFS{}
			names, err := fs.Glob(migrationFiles, "migrations/*.sql")
			require.NoError(err)
			for _, name := range names {
				data, readErr := migrationFiles.ReadFile(name)
				require.NoError(readErr)
				if tc.preview && name == "migrations/000054_federated_spoke_preparation.up.sql" {
					data = []byte(previewNames.Replace(string(data)))
				}
				files[name] = &fstest.MapFile{Data: data}
			}
			raw, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)")
			require.NoError(err)
			t.Cleanup(func() { raw.Close() })
			source, err := iofs.New(files, "migrations")
			require.NoError(err)
			driver, err := migratesqlite.WithInstance(raw, &migratesqlite.Config{MigrationsTable: migrationTableName})
			require.NoError(err)
			migrator, err := migrate.NewWithInstance("iofs", source, "sqlite", driver)
			require.NoError(err)
			require.NoError(migrator.Migrate(54))
			seed := `
				UPDATE forge_spoke_preparation SET
					phase = 'quiescing', enrollment_id = 'enrollment-a',
					hub_node_id = 'hub-a', local_node_id = 'node-a',
					protocol_version = 1, ack_generation = 7, drain_ack_generation = 6,
					preparation_digest = 'pending-digest', preparation_seal = 'pending-seal',
					started_at = '2026-01-01T12:00:00Z';
				INSERT INTO forge_spoke_preparation_receipts VALUES
					('review_draft', 'draft-a', 'content-a', 'receipt-a', '2026-01-01T12:00:00Z'),
					('workflow_state', 'workflow-a', 'content-b', 'receipt-b', '2026-01-01T12:00:00Z');
				INSERT INTO forge_spoke_preparation_seals VALUES
					('sealed-enrollment', 'node-a', 'hub-a', 1, 54, 'receipts-digest',
					 6, 'sealed-digest', 'sealed-value', '2026-01-01T12:00:00Z');
				INSERT INTO forge_notification_items (
					platform, platform_host, platform_notification_id, repo_owner, repo_name,
					subject_type, subject_title, item_number, item_type, reason, unread,
					source_updated_at, synced_at, source_ack_queued_at
				) VALUES (
					'github', 'github.com', 'existing', 'acme', 'widget',
					'PullRequest', 'Existing notification', 7, 'pr', 'mention', 1,
					'2026-01-01T12:00:00Z', '2026-01-01T12:00:00Z', '2026-01-01T12:00:00Z'
				);
				UPDATE forge_spoke_preparation SET updated_at = '2026-01-01T12:00:00Z';
			`
			if tc.preview {
				seed = previewNames.Replace(seed)
			}
			_, err = raw.ExecContext(t.Context(), seed)
			require.NoError(err)
			err = migrator.Migrate(tc.version)
			if !errors.Is(err, migrate.ErrNoChange) {
				require.NoError(err)
			}
			require.NoError(raw.Close())

			database, err := Open(dbPath)
			require.NoError(err)
			t.Cleanup(func() { database.Close() })
			stamp := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			queryAt := stamp.Add(24 * time.Hour)
			state, err := database.GetSpokePreparation(t.Context())
			require.NoError(err)
			assert.Equal(SpokePreparationState{
				Phase:        SpokePreparationQuiescing,
				EnrollmentID: "enrollment-a", HubNodeID: "hub-a", LocalNodeID: "node-a", ProtocolVersion: 1,
				MigrationVersion: 54, AckGeneration: 8, DrainAckGeneration: new(int64(6)),
				PreparationDigest: "pending-digest", PreparationSeal: "pending-seal",
				StartedAt: &stamp, UpdatedAt: stamp,
			}, state)
			receipts, err := database.ListSpokePreparationReceipts(t.Context())
			require.NoError(err)
			assert.Equal([]SpokePreparationReceipt{
				{StateKind: "review_draft", SourceKey: "draft-a", ContentDigest: "content-a", HubReceipt: "receipt-a", ImportedAt: stamp},
				{StateKind: "workflow_state", SourceKey: "workflow-a", ContentDigest: "content-b", HubReceipt: "receipt-b", ImportedAt: stamp},
			}, receipts)
			seal, err := database.GetSpokePreparationSeal(t.Context(), "sealed-enrollment")
			require.NoError(err)
			assert.Equal(&SpokePreparationSeal{
				EnrollmentID: "sealed-enrollment", NodeID: "node-a", HubNodeID: "hub-a",
				ProtocolVersion: 1, MigrationVersion: 54, ReceiptsDigest: "receipts-digest",
				DrainedAckGeneration: 6, PreparationDigest: "sealed-digest",
				Seal: "sealed-value", CreatedAt: stamp,
			}, seal)

			queued, err := database.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, queryAt)
			require.NoError(err)
			assert.Empty(queued, "preserve the frozen acknowledgement boundary")
			require.NoError(database.AbortSpokePreparation(t.Context()))
			queued, err = database.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, queryAt)
			require.NoError(err)
			require.Len(queued, 1)
			assert.Equal("existing", queued[0].PlatformNotificationID)

			seedNotificationRepo(t, database)
			insert := notificationFixture("insert-admission", "mention", stamp)
			insert.SourceAckQueuedAt = &stamp
			require.NoError(database.UpsertNotifications(t.Context(), []Notification{
				insert, notificationFixture("update-admission", "mention", stamp),
			}))
			items, err := database.ListNotifications(t.Context(), ListNotificationsOpts{State: "all"})
			require.NoError(err)
			var updateID int64
			for _, item := range items {
				if item.PlatformNotificationID == "update-admission" {
					updateID = item.ID
				}
			}
			require.NotZero(updateID)
			_, err = database.QueueNotificationIDsRead(t.Context(), []int64{updateID}, stamp)
			require.NoError(err)
			queued, err = database.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, queryAt)
			require.NoError(err)
			assert.Len(queued, 3)
			state, err = database.GetSpokePreparation(t.Context())
			require.NoError(err)
			assert.Equal(int64(10), state.AckGeneration)
			assertDatabaseIntegrityForTest(t, database.ReadDB())
			require.NoError(database.Close())

			reopened, err := Open(dbPath)
			require.NoError(err)
			t.Cleanup(func() { reopened.Close() })
			state, err = reopened.GetSpokePreparation(t.Context())
			require.NoError(err)
			assert.Equal(int64(10), state.AckGeneration)
		})
	}
}
