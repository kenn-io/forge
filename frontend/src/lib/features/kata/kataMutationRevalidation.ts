export interface KataSnapshotReplacementResult {
  replacementAccepted: boolean;
  replacementError: string | null;
}

export interface KataMutationRevalidation<T> {
  acknowledgement: T;
  replacement: Promise<KataSnapshotReplacementResult>;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export async function acknowledgeKataMutationThenRevalidate<T>(
  mutate: () => Promise<T>,
  revalidate: () => Promise<boolean>,
  onAcknowledged?: ((acknowledgement: T) => void) | undefined,
): Promise<KataMutationRevalidation<T>> {
  const acknowledgement = await mutate();
  onAcknowledged?.(acknowledgement);
  return {
    acknowledgement,
    replacement: (async () => {
      try {
        const replacementAccepted = await revalidate();
        return {
          replacementAccepted,
          replacementError: replacementAccepted ? null : "Kata snapshot replacement was not accepted.",
        };
      } catch (error) {
        return {
          replacementAccepted: false,
          replacementError: errorMessage(error),
        };
      }
    })(),
  };
}
