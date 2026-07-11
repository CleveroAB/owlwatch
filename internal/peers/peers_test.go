package peers

import (
	"strings"
	"testing"
)

// wantPeer is the comparable projection of a Peer for table tests.
type wantPeer struct {
	id, name, url, token string
}

func TestParsePeers(t *testing.T) {
	tests := []struct {
		name         string
		env          string
		defaultToken string
		want         []wantPeer
		wantErr      string // substring of the expected error; "" = success
	}{
		{
			name: "empty input",
			env:  "",
			want: nil,
		},
		{
			name: "whitespace-only input",
			env:  "   ",
			want: nil,
		},
		{
			name: "only separators",
			env:  " , ",
			want: nil,
		},
		{
			name: "single peer",
			env:  "web1=http://10.0.0.11:8080",
			want: []wantPeer{{"web1", "web1", "http://10.0.0.11:8080", ""}},
		},
		{
			name:         "default token fills empty",
			env:          "web1=http://a",
			defaultToken: "def",
			want:         []wantPeer{{"web1", "web1", "http://a", "def"}},
		},
		{
			name:         "per-peer token override",
			env:          "web1=http://a,db1=https://db.example.com|s3cret",
			defaultToken: "def",
			want: []wantPeer{
				{"web1", "web1", "http://a", "def"},
				{"db1", "db1", "https://db.example.com", "s3cret"},
			},
		},
		{
			name:         "empty override disables the default token",
			env:          "db1=http://a|",
			defaultToken: "def",
			want:         []wantPeer{{"db1", "db1", "http://a", ""}},
		},
		{
			name: "override token may contain =",
			env:  "a=http://x|t=ok=",
			want: []wantPeer{{"a", "a", "http://x", "t=ok="}},
		},
		{
			name: "configured order is preserved",
			env:  "b=http://b,a=http://a",
			want: []wantPeer{{"b", "b", "http://b", ""}, {"a", "a", "http://a", ""}},
		},
		{
			name: "name is lowercased for the id, casing kept for display",
			env:  "Web1=http://a",
			want: []wantPeer{{"web1", "Web1", "http://a", ""}},
		},
		{
			name: "trailing slash stripped",
			env:  "a=http://x:8080/",
			want: []wantPeer{{"a", "a", "http://x:8080", ""}},
		},
		{
			name: "surrounding whitespace trimmed",
			env:  " a = http://x , b = http://y ",
			want: []wantPeer{{"a", "a", "http://x", ""}, {"b", "b", "http://y", ""}},
		},
		{
			name: "trailing comma tolerated",
			env:  "a=http://x,",
			want: []wantPeer{{"a", "a", "http://x", ""}},
		},
		{
			name:    "missing =",
			env:     "web1",
			wantErr: "name=url",
		},
		{
			name:    "empty name",
			env:     "=http://x",
			wantErr: "invalid name",
		},
		{
			name:    "underscore in name",
			env:     "web_1=http://x",
			wantErr: "invalid name",
		},
		{
			name:    "name too long",
			env:     strings.Repeat("a", 33) + "=http://x",
			wantErr: "invalid name",
		},
		{
			name:    "reserved name local",
			env:     "local=http://x",
			wantErr: "reserved",
		},
		{
			name:    "reserved name overview, case-insensitive",
			env:     "Overview=http://x",
			wantErr: "reserved",
		},
		{
			name:    "duplicate names",
			env:     "a=http://x,a=http://y",
			wantErr: "duplicate",
		},
		{
			name:    "duplicate names after lowercasing",
			env:     "a=http://x,A=http://y",
			wantErr: "duplicate",
		},
		{
			name:    "url with path",
			env:     "a=http://x/api",
			wantErr: "path",
		},
		{
			name:    "url with query",
			env:     "a=http://x?b=1",
			wantErr: "query",
		},
		{
			name:    "url with fragment",
			env:     "a=http://x#frag",
			wantErr: "fragment",
		},
		{
			name:    "url without scheme",
			env:     "a=10.0.0.5:8080",
			wantErr: "invalid url",
		},
		{
			name:    "url with non-http scheme",
			env:     "a=ftp://x",
			wantErr: "scheme",
		},
		{
			name:    "empty url",
			env:     "a=",
			wantErr: "invalid url",
		},
		{
			name:    "url without host",
			env:     "a=http://",
			wantErr: "missing host",
		},
		{
			name:    "url with userinfo",
			env:     "a=https://alice:hunter2@db.example",
			wantErr: "|token suffix",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePeers(tt.env, tt.defaultToken)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ParsePeers(%q) = %v, want error containing %q", tt.env, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParsePeers(%q) error = %q, want it to contain %q", tt.env, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePeers(%q) unexpected error: %v", tt.env, err)
			}
			if tt.want == nil {
				if got != nil {
					t.Fatalf("ParsePeers(%q) = %v, want nil", tt.env, got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParsePeers(%q) returned %d peers, want %d", tt.env, len(got), len(tt.want))
			}
			for i, w := range tt.want {
				p := got[i]
				if p.ID != w.id || p.Name != w.name || p.URL.String() != w.url || p.Token != w.token {
					t.Errorf("peer %d = {ID:%q Name:%q URL:%q Token:%q}, want {ID:%q Name:%q URL:%q Token:%q}",
						i, p.ID, p.Name, p.URL, p.Token, w.id, w.name, w.url, w.token)
				}
			}
		})
	}
}

func TestParsePeersUserinfoErrorRedactsPassword(t *testing.T) {
	_, err := ParsePeers("a=https://alice:hunter2@db.example", "")
	if err == nil {
		t.Fatal("ParsePeers accepted a userinfo URL")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q leaks the password", err)
	}
}
