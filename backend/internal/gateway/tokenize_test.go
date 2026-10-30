package gateway

import (
	"slices"
	"testing"
)

func TestTokenizeSplitsNamesHoweverTheyAreSpelled(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"read_file", []string{"read", "file"}},
		{"read-file", []string{"read", "file"}},
		{"readFile", []string{"read", "file"}},
		{"ReadFile", []string{"read", "file"}},
		{"HTTPServer", []string{"http", "server"}},
		{"getV2Token", []string{"get", "v2", "token"}},
		{"slack_list_channels", tokenize("slack list channel")},
		{"read the contents of a file from disk", tokenize("read the content of a file from disk")},
		{"base64", []string{"base64"}},
		{"", nil},
		{"__--  ", nil},
	} {
		if got := tokenize(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("tokenize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A query in one form has to find a name or a description in another:
// "issues" is how a caller says list_issues, "searching" how it says
// search_files, "committed" how it says git_commit. What the stem itself
// looks like does not matter, only that every form lands on the same one.
func TestTokenizeFindsEveryFormOfAWord(t *testing.T) {
	for _, forms := range [][]string{
		{"issue", "issues"},
		{"file", "files"},
		{"repository", "repositories"},
		{"branch", "branches"},
		{"message", "messages"},
		{"search", "searches", "searching", "searched"},
		{"delete", "deletes", "deleting", "deleted"},
		{"commit", "commits", "committing", "committed"},
		{"create", "creating", "created"},
		{"list", "lists", "listing"},
	} {
		want := tokenize(forms[0])
		for _, form := range forms[1:] {
			if got := tokenize(form); !slices.Equal(got, want) {
				t.Errorf("tokenize(%q) = %q, want %q, the same as %q", form, got, want, forms[0])
			}
		}
	}
}

// The other half of the same rule: a stem that swallowed different words
// would make one tool answer for another.
func TestTokenizeKeepsDifferentWordsApart(t *testing.T) {
	for _, pair := range [][2]string{
		{"read", "ready"},
		{"post", "postgres"},
		{"user", "use"},
		{"page", "pages2"},
		{"status", "state"},
	} {
		if a, b := tokenize(pair[0]), tokenize(pair[1]); slices.Equal(a, b) {
			t.Errorf("tokenize(%q) and tokenize(%q) are both %q", pair[0], pair[1], a)
		}
	}
}

// Identifiers with digits are not English words and are kept as written.
func TestTokenizeLeavesIdentifiersAlone(t *testing.T) {
	for _, in := range []string{"v2", "s3", "base64", "oauth2"} {
		if got := tokenize(in); !slices.Equal(got, []string{in}) {
			t.Errorf("tokenize(%q) = %q, want it unchanged", in, got)
		}
	}
}

func TestTokenizeCutsCJKIntoPairs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"读取文件", []string{"读取", "取文", "文件"}},
		{"读取指定城市的天气", []string{"读取", "取指", "指定", "定城", "城市", "市的", "的天", "天气"}},
		{"诗", []string{"诗"}},
		{"古诗", []string{"古诗"}},
		// Punctuation separates runs rather than joining them into a pair.
		{"温度、湿度", []string{"温度", "湿度"}},
		// Scripts are split from one another.
		{"读取file", []string{"读取", "file"}},
		{"weather 天气", []string{"weather", "天气"}},
		{"東京タワー", []string{"東京", "京タ", "タワ", "ワー"}},
		{"서울 날씨", []string{"서울", "날씨"}},
	} {
		if got := tokenize(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("tokenize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
