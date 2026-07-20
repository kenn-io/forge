// Package gitfake provides shell fragments for fake git executables used by
// tests.
package gitfake

// CredentialHelperRunner defines run_credential_helper, which follows Git's
// credential.helper dispatch rules for inline shell snippets and executable
// paths. The operation and any later arguments are appended to the helper.
const CredentialHelperRunner = `run_credential_helper() {
	helper="$1"
	shift
	case "$helper" in
		!*) /bin/sh -c "${helper#?} \"\$@\"" "$helper" "$@" ;;
		*) "$helper" "$@" ;;
	esac
}
`
