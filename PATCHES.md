# Fork patches

This is a fork of `github.com/emersion/go-imap/v2` used by
seed-mailbox-listener through a `replace` directive:

```
replace github.com/emersion/go-imap/v2 => github.com/lucas-tech-cyber/go-imap/v2 <tag>
```

The module path in `go.mod` is deliberately left as
`github.com/emersion/go-imap/v2`, so imports stay unchanged.

## Base

- Upstream branch `v2` at commit **`46e71ca`** ("readme: link to
  CONTRIBUTING.md"), 9 commits after the `v2.0.0-beta.8` tag.
- Patches live on the `rawkeys` branch; releases are tagged
  `v2.0.0-rawkeys.N`.

## Change: raw search keys (`SearchCriteria.RawKeys`)

**Why.** Gmail inbox tabs (Promotions, Updates, Social, Forums) are only
visible over IMAP through the vendor search key
`X-GM-RAW "category:<tab>"`. go-imap has no way to put an arbitrary key into
`SEARCH`, and the maintainer does not accept Gmail extensions (PRs #512, #759).

**What.**

1. `search.go`: new field `RawKeys []string` on `imap.SearchCriteria`, and
   `SearchCriteria.And` concatenates it like the other slice fields.
2. `imapclient/search.go`: `writeSearchKey` writes every raw key verbatim
   (space-separated, as an atom) **after** all other keys, before the
   `ALL` fallback. With `RawKeys` empty the command is byte-for-byte unchanged.

The caller is responsible for quoting and escaping, so on the wire it is
exactly:

```
UID SEARCH UID 10:20 X-GM-RAW "category:promotions"
```

Known limitations (deliberately not handled, to keep the patch minimal):
raw keys are not considered when deciding whether `CHARSET UTF-8` is needed,
and the server side (`imapserver`) does not parse them.

The wire format is tested in seed-mailbox-listener
(`internal/imapconn/imapconn_test.go`, `TestUIDSearch_RawKeysWireFormat`).

## Diff

```diff
--- a/imapclient/search.go
+++ b/imapclient/search.go
@@ -263,6 +263,11 @@ func writeSearchKey(enc *imapwire.Encoder, criteria *imap.SearchCriteria) {
 		enc.Special(')')
 	}
 
+	// rawkeys patch: raw keys go last, written as is.
+	for _, raw := range criteria.RawKeys {
+		encodeItem().Atom(raw)
+	}
+
 	if firstItem {
 		enc.Atom("ALL")
 	}
--- a/search.go
+++ b/search.go
@@ -59,6 +59,11 @@ type SearchCriteria struct {
 	Or  [][2]SearchCriteria
 
 	ModSeq *SearchCriteriaModSeq // requires CONDSTORE
+
+	// RawKeys are appended verbatim, space-separated, after all other search
+	// keys. The caller is responsible for quoting and escaping. Intended for
+	// vendor extensions such as Gmail's X-GM-RAW. (rawkeys patch)
+	RawKeys []string
 }
 
 // And intersects two search criteria.
@@ -87,6 +92,8 @@ func (criteria *SearchCriteria) And(other *SearchCriteria) {
 
 	criteria.Not = append(criteria.Not, other.Not...)
 	criteria.Or = append(criteria.Or, other.Or...)
+
+	criteria.RawKeys = append(criteria.RawKeys, other.RawKeys...) // rawkeys patch
 }
 
 func intersectSince(t1, t2 time.Time) time.Time {
```

## Moving to a new upstream version

1. `git fetch upstream` (`https://github.com/emersion/go-imap`) and rebase
   the `rawkeys` branch onto the new upstream commit or tag.
2. If the rebase conflicts, re-apply the diff above: search the new
   `writeSearchKey` for the `ALL` fallback; the raw keys must stay the last
   keys written.
3. Update the base commit in this file, run `go test ./...`, tag the next
   `v2.0.0-rawkeys.N` and push the branch and the tag.
4. In seed-mailbox-listener bump the tag in the `replace` line and run
   `make tidy && make check && make test-integration`.
