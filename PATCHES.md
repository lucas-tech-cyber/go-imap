# Fork patches

This is a fork of `github.com/emersion/go-imap/v2` used by
seed-mailbox-listener through a `replace` directive:

```
replace github.com/emersion/go-imap/v2 => github.com/lucas-tech-cyber/go-imap/v2 <tag>
```

The module path in `go.mod` is deliberately left as
`github.com/emersion/go-imap/v2`, so imports stay unchanged.

## Base

- Upstream branch `v2` at commit **`99eeae5`** ("imapserver: don't
  expunge on CLOSE of a read-only mailbox"), which is the `v2.0.0-beta.9`
  tag.
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

## Change: Gmail FETCH items (`X-GM-MSGID`, `X-GM-THRID`, `X-GM-LABELS`)

**Why.** For mailboxes that read `[Gmail]/All Mail`, seed-mailbox-listener
stores each message's Gmail labels, plus the Gmail message and thread IDs for
every Gmail folder. go-imap fails the whole FETCH response (and tears down the
connection) on an unknown `msg-att`, so these items cannot be requested at all
without the patch.

**What.** Code is marked `gmail-fetch patch`.

1. `fetch.go`: `imap.FetchOptions` gets `GmailMessageID`, `GmailThreadID`,
   `GmailLabels bool`. They are written as `X-GM-MSGID`, `X-GM-THRID`,
   `X-GM-LABELS`, in that order, after the other atoms.
2. `imapclient/fetch.go`: the FETCH response parser handles the three items
   (`FetchItemDataGmailMessageID`, `FetchItemDataGmailThreadID`,
   `FetchItemDataGmailLabels`), and `FetchMessageBuffer` gets
   `GmailMessageID`, `GmailThreadID uint64` and `GmailLabels []string`.

Semantics:

- IDs are unsigned 64-bit, parsed with `strconv.ParseUint` (via
  `Decoder.ModSeq`): no signed overflow, values up to
  `18446744073709551615`. `0` means "not fetched".
- Labels: system labels keep their backslash (`\Inbox`, `\Sent`,
  `\Important`…), whether sent as an atom or quoted (`"\\Important"`).
  Other labels (atom, quoted or literal) are **decoded here** from modified
  UTF-7 to UTF-8, the same as mailbox names in LIST. A label that is not valid
  modified UTF-7 is returned as sent instead of failing: a parse error would
  close the connection over one label. `GmailLabels` is `nil` when not
  fetched and a non-nil (possibly empty) slice when fetched.
- The server side (`imapserver`) does not support these items.

Tests: `imapclient/gmail_test.go` (scripted server: request wire format,
IDs above `2^53 - 1` and the unsigned-64 maximum, system/quoted/literal/
non-ASCII labels, empty list, a stray space after `(`).

### Diff

```diff
--- a/fetch.go
+++ b/fetch.go
@@ -20,6 +20,11 @@ type FetchOptions struct {
 	BinarySectionSize []*FetchItemBinarySectionSize // requires IMAP4rev2 or BINARY
 	ModSeq            bool                          // requires CONDSTORE
 
+	// Gmail extensions, require X-GM-EXT-1. (gmail-fetch patch)
+	GmailMessageID bool // X-GM-MSGID
+	GmailThreadID  bool // X-GM-THRID
+	GmailLabels    bool // X-GM-LABELS
+
 	ChangedSince uint64 // requires CONDSTORE
 }
 
--- a/imapclient/fetch.go
+++ b/imapclient/fetch.go
@@ -10,6 +10,7 @@ import (
 	"github.com/emersion/go-imap/v2"
 	"github.com/emersion/go-imap/v2/internal"
 	"github.com/emersion/go-imap/v2/internal/imapwire"
+	"github.com/emersion/go-imap/v2/internal/utf7"
 	"github.com/emersion/go-message/mail"
 )
 
@@ -64,6 +65,17 @@ func writeFetchItems(enc *imapwire.Encoder, numKind imapwire.NumKind, options *i
 		}
 	}
 
+	// gmail-fetch patch: Gmail items, in a fixed order.
+	if options.GmailMessageID {
+		listEnc.Item().Atom("X-GM-MSGID")
+	}
+	if options.GmailThreadID {
+		listEnc.Item().Atom("X-GM-THRID")
+	}
+	if options.GmailLabels {
+		listEnc.Item().Atom("X-GM-LABELS")
+	}
+
 	for _, bs := range options.BodySection {
 		writeFetchItemBodySection(listEnc.Item(), bs)
 	}
@@ -479,6 +491,38 @@ type FetchItemDataModSeq struct {
 
 func (FetchItemDataModSeq) fetchItemData() {}
 
+// FetchItemDataGmailMessageID holds data returned by FETCH X-GM-MSGID.
+//
+// This requires the X-GM-EXT-1 extension. (gmail-fetch patch)
+type FetchItemDataGmailMessageID struct {
+	MessageID uint64
+}
+
+func (FetchItemDataGmailMessageID) fetchItemData() {}
+
+// FetchItemDataGmailThreadID holds data returned by FETCH X-GM-THRID.
+//
+// This requires the X-GM-EXT-1 extension. (gmail-fetch patch)
+type FetchItemDataGmailThreadID struct {
+	ThreadID uint64
+}
+
+func (FetchItemDataGmailThreadID) fetchItemData() {}
+
+// FetchItemDataGmailLabels holds data returned by FETCH X-GM-LABELS.
+//
+// System labels keep their backslash (`\Inbox`, `\Sent`, `\Important`...).
+// Other labels are decoded from modified UTF-7 to UTF-8; a label that is not
+// valid modified UTF-7 is returned as sent by the server. Labels is non-nil,
+// possibly empty.
+//
+// This requires the X-GM-EXT-1 extension. (gmail-fetch patch)
+type FetchItemDataGmailLabels struct {
+	Labels []string
+}
+
+func (FetchItemDataGmailLabels) fetchItemData() {}
+
 // FetchBodySectionBuffer is a buffer for the data returned by
 // FetchItemBodySection.
 type FetchBodySectionBuffer struct {
@@ -508,6 +552,12 @@ type FetchMessageBuffer struct {
 	BinarySection     []FetchBinarySectionBuffer
 	BinarySectionSize []FetchItemDataBinarySectionSize
 	ModSeq            uint64 // requires CONDSTORE
+
+	// Gmail extensions, require X-GM-EXT-1; zero when not fetched.
+	// (gmail-fetch patch)
+	GmailMessageID uint64
+	GmailThreadID  uint64
+	GmailLabels    []string // nil when not fetched; see FetchItemDataGmailLabels
 }
 
 func (buf *FetchMessageBuffer) populateItemData(item FetchItemData) error {
@@ -554,6 +604,12 @@ func (buf *FetchMessageBuffer) populateItemData(item FetchItemData) error {
 		buf.BinarySectionSize = append(buf.BinarySectionSize, item)
 	case FetchItemDataModSeq:
 		buf.ModSeq = item.ModSeq
+	case FetchItemDataGmailMessageID:
+		buf.GmailMessageID = item.MessageID
+	case FetchItemDataGmailThreadID:
+		buf.GmailThreadID = item.ThreadID
+	case FetchItemDataGmailLabels:
+		buf.GmailLabels = item.Labels
 	default:
 		panic(fmt.Errorf("unsupported fetch item data %T", item))
 	}
@@ -801,6 +857,26 @@ func (c *Client) handleFetch(seqNum uint32) error {
 				return dec.Err()
 			}
 			item = FetchItemDataModSeq{ModSeq: modSeq}
+		case "X-GM-MSGID", "X-GM-THRID": // gmail-fetch patch
+			// Unsigned 64-bit; ModSeq parses a uint64 without overflow.
+			var id uint64
+			if !dec.ExpectSP() || !dec.Expect(dec.ModSeq(&id), attName) {
+				return dec.Err()
+			}
+			if attName == "X-GM-MSGID" {
+				item = FetchItemDataGmailMessageID{MessageID: id}
+			} else {
+				item = FetchItemDataGmailThreadID{ThreadID: id}
+			}
+		case "X-GM-LABELS": // gmail-fetch patch
+			if !dec.ExpectSP() {
+				return dec.Err()
+			}
+			labels, err := readGmailLabels(dec)
+			if err != nil {
+				return fmt.Errorf("in X-GM-LABELS: %w", err)
+			}
+			item = FetchItemDataGmailLabels{Labels: labels}
 		default:
 			return fmt.Errorf("unsupported msg-att name: %q", attName)
 		}
@@ -824,6 +900,39 @@ func (c *Client) handleFetch(seqNum uint32) error {
 	})
 }
 
+// readGmailLabels reads an X-GM-LABELS list: system labels as `\Name`
+// atoms, other labels as astrings (atom, quoted or literal) in modified
+// UTF-7. (gmail-fetch patch)
+func readGmailLabels(dec *imapwire.Decoder) ([]string, error) {
+	labels := []string{}
+	err := dec.ExpectList(func() error {
+		// Tolerate a stray leading space, as ExpectFlagList does.
+		dec.SP()
+
+		var label string
+		if dec.Special('\\') {
+			if !dec.ExpectAtom(&label) {
+				return dec.Err()
+			}
+			labels = append(labels, `\`+label)
+			return nil
+		}
+		if !dec.ExpectAString(&label) {
+			return dec.Err()
+		}
+		// Only labels with '&' need decoding. Decoding failure is not fatal:
+		// a parse error here would tear down the connection over one label.
+		if strings.IndexByte(label, '&') >= 0 {
+			if decoded, err := utf7.Decode(label); err == nil {
+				label = decoded
+			}
+		}
+		labels = append(labels, label)
+		return nil
+	})
+	return labels, err
+}
+
 func isMsgAttNameChar(ch byte) bool {
 	return ch != '[' && imapwire.IsAtomChar(ch)
 }
```

## Moving to a new upstream version

1. `git fetch upstream` (`https://github.com/emersion/go-imap`) and rebase
   the `rawkeys` branch onto the new upstream commit or tag.
2. If the rebase conflicts, re-apply the diff above: search the new
   `writeSearchKey` for the `ALL` fallback; the raw keys must stay the last
   keys written. Re-apply the Gmail FETCH diff the same way: the items go
   after the other atoms in `writeFetchItems`, and the parser cases next to
   `MODSEQ` in `handleFetch`.
3. Update the base commit in this file, run `go test ./...`, tag the next
   `v2.0.0-rawkeys.N` and push the branch and the tag.
4. In seed-mailbox-listener bump the tag in the `replace` line and run
   `make tidy && make check && make test-integration`.
