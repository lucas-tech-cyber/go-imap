package imapclient_test

import (
	"bufio"
	"net"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// fetchFromScriptedServer runs one FETCH against a fake server that answers
// with fetchResp (one untagged FETCH response, without the trailing CRLF). It
// returns the command line the client sent and the collected messages.
func fetchFromScriptedServer(t *testing.T, numSet imap.NumSet, options *imap.FetchOptions, fetchResp string) (string, []*imapclient.FetchMessageBuffer) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	client := imapclient.New(clientConn, nil)
	defer client.Close()

	cmdLine := make(chan string, 1)
	go func() {
		defer serverConn.Close()
		w := bufio.NewWriter(serverConn)
		r := bufio.NewReader(serverConn)
		w.WriteString("* OK [CAPABILITY IMAP4rev1 X-GM-EXT-1] ready\r\n")
		w.Flush()

		line, err := r.ReadString('\n')
		if err != nil {
			cmdLine <- ""
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmdLine <- line

		tag, _, _ := strings.Cut(line, " ")
		w.WriteString(fetchResp + "\r\n")
		w.WriteString(tag + " OK done\r\n")
		w.Flush()

		// Keep the connection open until the client closes it.
		r.ReadString('\n')
	}()

	msgs, err := client.Fetch(numSet, options).Collect()
	if err != nil {
		t.Fatalf("Fetch().Collect() = %v", err)
	}
	return <-cmdLine, msgs
}

func TestFetchGmailIDs(t *testing.T) {
	const (
		msgID = uint64(18446744073709551615) // unsigned 64-bit maximum
		thrID = uint64(9007199254740993)     // 2^53 + 1, above JavaScript's safe integers
	)

	cmd, msgs := fetchFromScriptedServer(t, imap.UIDSetNum(42), &imap.FetchOptions{
		GmailMessageID: true,
		GmailThreadID:  true,
	}, "* 1 FETCH (UID 42 X-GM-MSGID 18446744073709551615 X-GM-THRID 9007199254740993)")

	if !strings.HasSuffix(cmd, " UID FETCH 42 (UID X-GM-MSGID X-GM-THRID)") {
		t.Errorf("command = %q, want UID FETCH 42 (UID X-GM-MSGID X-GM-THRID)", cmd)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if got := msgs[0].GmailMessageID; got != msgID {
		t.Errorf("GmailMessageID = %d, want %d", got, msgID)
	}
	if got := msgs[0].GmailThreadID; got != thrID {
		t.Errorf("GmailThreadID = %d, want %d", got, thrID)
	}
}

func TestFetchGmailLabels(t *testing.T) {
	cmd, msgs := fetchFromScriptedServer(t, imap.UIDSetNum(7, 8, 9), &imap.FetchOptions{
		GmailLabels: true,
	}, "* 1 FETCH (UID 7 X-GM-LABELS (\\Inbox \"\\\\Important\" \\Sent Work \"Team updates\" \"say \\\"hi\\\"\" \"&BB4EQgRHBFEEQgRL-\" {6}\r\nlit lb \"R&D\"))\r\n"+
		"* 2 FETCH (UID 8 X-GM-LABELS ())\r\n"+
		"* 3 FETCH (UID 9 X-GM-LABELS ( \\Inbox))")

	if !strings.HasSuffix(cmd, " UID FETCH 7:9 (UID X-GM-LABELS)") {
		t.Errorf("command = %q, want UID FETCH 7:9 (UID X-GM-LABELS)", cmd)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}

	want := []string{
		`\Inbox`,
		`\Important`,
		`\Sent`,
		"Work",
		"Team updates",
		`say "hi"`,
		"Отчёты", // decoded from modified UTF-7
		"lit lb", // literal
		"R&D",    // not valid modified UTF-7, kept as sent
	}
	if got := msgs[0].GmailLabels; !equalStrings(got, want) {
		t.Errorf("GmailLabels = %q, want %q", got, want)
	}

	if got := msgs[1].GmailLabels; got == nil || len(got) != 0 {
		t.Errorf("GmailLabels for () = %#v, want an empty non-nil slice", got)
	}

	// A stray space after "(" is tolerated, as in FLAGS.
	if got, want := msgs[2].GmailLabels, []string{`\Inbox`}; !equalStrings(got, want) {
		t.Errorf("GmailLabels for ( \\Inbox) = %q, want %q", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
