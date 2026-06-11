package msgvault

import (
	"context"
	"fmt"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type xssFixture struct {
	name             string
	input            string
	mustNotContain   []string
	mustContain      []string
	remoteImageCount int
}

func TestSanitizerSkeleton(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	s := NewSanitizer()
	require.NotNil(s)
	assert.NotNil(s.policy)
	assert.NotNil(s.cache)
	_, err := s.Sanitize(context.Background(), 1, "", 0)
	assert.NoError(err)
}

func TestSanitizerBumpGeneration(t *testing.T) {
	s := NewSanitizer()
	before := s.generation.Load()
	s.BumpGeneration()
	Assert.Equal(t, before+1, s.generation.Load())
}

func TestSanitizeXSSCanon(t *testing.T) {
	s := NewSanitizer()
	cases := []xssFixture{
		{name: "script_tag", input: `<script>alert(1)</script>hi`, mustNotContain: []string{"<script", "alert(1)"}, mustContain: []string{"hi"}},
		{name: "inline_event_handler", input: `<div onclick="x=1">x</div>`, mustNotContain: []string{"onclick"}, mustContain: []string{"x"}},
		{name: "javascript_href", input: `<a href="javascript:alert(1)">x</a>`, mustNotContain: []string{"javascript:", "alert(1)"}, mustContain: []string{`target="_blank"`, `rel="noopener noreferrer"`}},
		{name: "vbscript_href", input: `<a href="vbscript:msgbox(1)">x</a>`, mustNotContain: []string{"vbscript:"}},
		{name: "javascript_src", input: `<img src="javascript:alert(1)">`, mustNotContain: []string{"javascript:", "alert(1)"}},
		{name: "data_url_html", input: `<img src="data:text/html,<script>alert(1)</script>">`, mustNotContain: []string{"text/html"}},
		{name: "data_url_svg", input: `<img src="data:image/svg+xml;base64,YWJj">`, mustNotContain: []string{"svg+xml"}},
		{name: "data_url_png", input: `<img src="data:image/png;base64,iVBORw0KGgo=">`, mustContain: []string{"data:image/png;base64,iVBORw0KGgo="}},
		{name: "style_tag", input: `<style>body{x:expression(alert(1))}</style>p`, mustNotContain: []string{"<style", "expression(", "alert(1)"}, mustContain: []string{"p"}},
		{name: "style_attr", input: `<p style="background:url(http://e.com/x)">x</p>`, mustNotContain: []string{"style=", "background:", "url("}, mustContain: []string{"x"}},
		{name: "svg_script", input: `<svg><script>alert(1)</script></svg>after`, mustNotContain: []string{"<svg", "<script", "alert(1)"}, mustContain: []string{"after"}},
		{name: "iframe_inline", input: `<iframe src="http://e.com"></iframe>after`, mustNotContain: []string{"<iframe"}, mustContain: []string{"after"}},
		{name: "base_href", input: `<base href="http://e.com/"><a href="/x">x</a>`, mustNotContain: []string{"<base", "/x"}, mustContain: []string{`target="_blank"`, `rel="noopener noreferrer"`, ">x</a>"}},
		{name: "object_embed", input: `<object data="a"></object><embed src="b">after`, mustNotContain: []string{"<object", "<embed"}, mustContain: []string{"after"}},
		{name: "form_input", input: `<form action="x"><input name="y"></form>after`, mustNotContain: []string{"<form", "<input"}, mustContain: []string{"after"}},
		{name: "meta_refresh", input: `<meta http-equiv="refresh" content="0;url=http://e.com">after`, mustNotContain: []string{"<meta"}, mustContain: []string{"after"}},
		{name: "link_stylesheet", input: `<link rel="stylesheet" href="http://e.com/x.css">after`, mustNotContain: []string{"<link"}, mustContain: []string{"after"}},
		{name: "ping_attr", input: `<a href="http://x" ping="http://t/p">x</a>`, mustNotContain: []string{"ping="}, mustContain: []string{`href="http://x"`}},
		{name: "mathml", input: `<math><mtext>m</mtext></math>after`, mustNotContain: []string{"<math", "<mtext"}, mustContain: []string{"after"}},
		{name: "mixed_case_script", input: `<ScRiPt>alert(1)</ScRiPt>after`, mustNotContain: []string{"alert(1)", "<script", "<ScRiPt"}, mustContain: []string{"after"}},
		{name: "entity_obfuscation", input: `<a href="java&#115;cript:alert(1)">x</a>`, mustNotContain: []string{"javascript:", "alert(1)"}},
		{name: "relative_href", input: `<a href="/local/path">x</a>`, mustNotContain: []string{`href="/local/path"`}, mustContain: []string{`target="_blank"`, `rel="noopener noreferrer"`, ">x</a>"}},
		{name: "srcset_dropped", input: `<img src="http://e.com/a" srcset="http://e.com/b 2x">`, mustNotContain: []string{"srcset", "http://e.com/a", "http://e.com/b"}, mustContain: []string{`data-remote-image-idx="0"`}, remoteImageCount: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			res, err := s.Sanitize(context.Background(), 42, tc.input, 0)
			require.NoError(err)
			for _, bad := range tc.mustNotContain {
				assert.NotContains(res.HTML, bad)
			}
			for _, good := range tc.mustContain {
				assert.Contains(res.HTML, good)
			}
			assert.Equal(tc.remoteImageCount, res.RemoteImageCount)
		})
	}
}

func TestSanitizeImageRewrites(t *testing.T) {
	s := NewSanitizer()

	t.Run("cid_rewrites_to_inline", func(t *testing.T) {
		res, err := s.Sanitize(context.Background(), 1234, `<img src="cid:abc">`, 0)
		require.NoError(t, err)
		Assert.Contains(t, res.HTML, `src="/api/v1/msgvault/messages/1234/inline?cid=abc"`)
	})

	t.Run("multiple_images_assign_indices_in_document_order", func(t *testing.T) {
		assert := Assert.New(t)
		require := require.New(t)
		res, err := s.Sanitize(context.Background(), 1, strings.Join([]string{
			`<img src="http://e.com/a">`,
			`<img src="cid:keepme">`,
			`<img src="http://e.com/b">`,
			`<img src="data:image/png;base64,iVBORw0KGgo=">`,
			`<img src="http://e.com/c">`,
		}, ""), 0)
		require.NoError(err)
		assert.Equal(3, res.RemoteImageCount)
		for i := range 3 {
			assert.Contains(res.HTML, fmt.Sprintf(`data-remote-image-idx="%d"`, i))
		}
	})

	t.Run("empty_src_no_handle", func(t *testing.T) {
		res, err := s.Sanitize(context.Background(), 1, `<img src=""><img>`, 0)
		require.NoError(t, err)
		Assert.Zero(t, res.RemoteImageCount)
	})
}

func TestSanitizeAnchorRewrites(t *testing.T) {
	s := NewSanitizer()
	cases := []struct {
		name  string
		input string
		want  []string
		not   []string
	}{
		{name: "http_keeps_href_adds_target_rel", input: `<a href="http://x.com/y">x</a>`, want: []string{`href="http://x.com/y"`, `target="_blank"`, `rel="noopener noreferrer"`}},
		{name: "mailto_keeps_href", input: `<a href="mailto:a@b.com">x</a>`, want: []string{`href="mailto:a@b.com"`, `target="_blank"`, `rel="noopener noreferrer"`}},
		{name: "tel_keeps_href", input: `<a href="tel:+15551234">x</a>`, want: []string{`href="tel:+15551234"`}},
		{name: "target_self_overwritten", input: `<a href="http://x" target="_self">x</a>`, want: []string{`target="_blank"`}, not: []string{`target="_self"`}},
		{name: "rel_opener_overwritten", input: `<a href="http://x" rel="opener">x</a>`, want: []string{`rel="noopener noreferrer"`}, not: []string{`rel="opener"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			res, err := s.Sanitize(context.Background(), 1, tc.input, 0)
			require.NoError(err)
			for _, want := range tc.want {
				assert.Contains(res.HTML, want)
			}
			for _, not := range tc.not {
				assert.NotContains(res.HTML, not)
			}
		})
	}
}

func TestSanitizeTokenDeterminism(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	s := newSanitizerForBasePathWithTokenKey("/", bytesOf(0x11, 32))
	in := `<p>x</p><img src="http://e.com/a"><img src="http://e.com/b">`

	a, err := s.Sanitize(context.Background(), 99, in, 0)
	require.NoError(err)
	b, err := s.Sanitize(context.Background(), 99, in, 0)
	require.NoError(err)
	assert.Equal(a.Token, b.Token)
	assert.Len(a.Token, 32)

	reordered := `<p>x</p><img src="http://e.com/b"><img src="http://e.com/a">`
	c, err := s.Sanitize(context.Background(), 99, reordered, 0)
	require.NoError(err)
	assert.NotEqual(a.Token, c.Token)

	d, err := s.Sanitize(context.Background(), 99, in, 1)
	require.NoError(err)
	assert.NotEqual(a.Token, d.Token)

	otherSecret := newSanitizerForBasePathWithTokenKey("/", bytesOf(0x22, 32))
	e, err := otherSecret.Sanitize(context.Background(), 99, in, 0)
	require.NoError(err)
	assert.NotEqual(a.Token, e.Token)
}

func bytesOf(value byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = value
	}
	return out
}

func TestSanitizeInputSizeCap(t *testing.T) {
	s := NewSanitizer()
	huge := strings.Repeat("a", sanitizeMaxInputBytes+1)
	_, err := s.Sanitize(context.Background(), 1, huge, 0)
	require.ErrorIs(t, err, errSanitizeInputTooLarge)
}

func TestSanitizeOutputSizeCap(t *testing.T) {
	s := NewSanitizer()
	huge := strings.Repeat("<p>x</p>", 200_000)
	_, err := s.Sanitize(context.Background(), 1, huge, 0)
	require.ErrorIs(t, err, errSanitizeOutputTooLarge)
}

func TestSanitizeTooManyRemoteImages(t *testing.T) {
	s := NewSanitizer()
	var sb strings.Builder
	for i := 0; i <= sanitizeMaxRemoteImages; i++ {
		fmt.Fprintf(&sb, `<img src="http://e.com/%d">`, i)
	}
	_, err := s.Sanitize(context.Background(), 1, sb.String(), 0)
	require.ErrorIs(t, err, errSanitizeTooManyImages)
}

func TestSanitizeRemoteImageURLBytesCap(t *testing.T) {
	s := NewSanitizer()
	long := strings.Repeat("a", 80*1024)
	in := fmt.Sprintf(
		`<img src="http://e.com/?q=%s"><img src="http://e.com/?q=%s"><img src="http://e.com/?q=%s"><img src="http://e.com/?q=%s">`,
		long, long, long, long,
	)
	_, err := s.Sanitize(context.Background(), 1, in, 0)
	require.ErrorIs(t, err, errSanitizeImageURLsTooBig)
}

func TestSanitizeRejectsDeeplyNestedHTML(t *testing.T) {
	s := NewSanitizer()
	input := strings.Repeat("<div>", sanitizeMaxDOMDepth+1) +
		"x" +
		strings.Repeat("</div>", sanitizeMaxDOMDepth+1)

	_, err := s.Sanitize(context.Background(), 1, input, 0)

	require.ErrorIs(t, err, errSanitizeDOMTooDeep)
}

func TestSanitizeStripsForgedDataRemoteImageIdx(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	s := NewSanitizer()
	res, err := s.Sanitize(context.Background(), 1, `<img src="http://e.com/a" data-remote-image-idx="99">`, 0)
	require.NoError(err)
	assert.Equal(1, res.RemoteImageCount)
	assert.Contains(res.HTML, `data-remote-image-idx="0"`)
	assert.NotContains(res.HTML, `data-remote-image-idx="99"`)
}

func TestSanitizePanicRecoveryShape(t *testing.T) {
	mustPanic := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%w: %v", errSanitizePanic, r)
			}
		}()
		panic("boom")
	}
	err := mustPanic()
	require.ErrorIs(t, err, errSanitizePanic)
}

func TestSafeInvariantURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		safe bool
	}{
		{"empty", "", true},
		{"relative inline path", "/api/v1/msgvault/messages/7/inline?cid=x", true},
		{"colon after slash stays relative", "/a/b:c", true},
		{"protocol relative", "//tracker.example/img.png", false},
		{"protocol relative backslash", `\\tracker.example/img.png`, false},
		{"protocol relative mixed slash", `/\tracker.example/img.png`, false},
		{"tab obfuscated protocol relative", "/\t/tracker.example/img.png", false},
		{"http", "http://example.com/a", true},
		{"https mixed case", "HTTPS://example.com/a", true},
		{"mailto", "mailto:a@example.com", true},
		{"tel", "tel:+15551234", true},
		{"data image png", "data:image/png;base64,iVBORw0KGgo=", true},
		{"data html", "data:text/html,<script>alert(1)</script>", false},
		{"data svg", "data:image/svg+xml;base64,YWJj", false},
		{"javascript", "javascript:alert(1)", false},
		{"vbscript mixed case", "VBScript:msgbox(1)", false},
		{"tab obfuscated scheme", "jav\tascript:alert(1)", false},
		{"newline obfuscated scheme", "java\nscript:alert(1)", false},
		{"leading control char", "\x01javascript:alert(1)", false},
		{"blob", "blob:https://example.com/uuid", false},
		{"file", "file:///etc/passwd", false},
		{"ftp", "ftp://example.com/x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Assert.Equal(t, tc.safe, safeInvariantURL(tc.url))
		})
	}
}
