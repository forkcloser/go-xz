# Releasing

This is a library; a release is a signed tag on `main`, and the Go module
proxy does the rest. There is no goreleaser configuration and no release
workflow: nothing is built or uploaded.

1. The release notes are the titles of the pull requests the release merges:
   check that each says what changed for a user, and that one that breaks a
   consumer carries the `breaking` label.
2. Upstream is reviewed through its head: every commit of upstream's
   `master` after the *Reviewed through* line of [`UPSTREAM.md`](./UPSTREAM.md)
   is ported or given its row there, and the line moves to that head.
3. `just lint && just test` on a clean tree, which runs the differential
   test against upstream, and CI green on `main` for the commit to be tagged
   (all five verify legs and the fuzz job).
4. Tag and push with the shared recipe, as a repository admin (the
   `limen:tags` ruleset's only bypass actor). It refuses a dirty tree, signs
   the tag — the organization's `CONTRIBUTING.md` requires it — and pushes
   the tag alone, never with `--tags`:

       just do release vX.Y.Z

5. Check what the proxy sees: `go list -m github.com/forkcloser/xz/v2@vX.Y.Z`
   from outside the tree.
6. Publish the GitHub release from the tag with the generated notes, so the
   tag has a human-readable page:

       gh release create vX.Y.Z --verify-tag --generate-notes

Versioning follows semantic versioning against the stability contract in the
README: within v2, only additive changes in minor releases, fixes in patch
releases. A breaking change means a `v3` module path.
