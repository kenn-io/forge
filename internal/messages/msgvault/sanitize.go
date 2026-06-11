package msgvault

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	// sanitizeMaxInputBytes is the raw-input cap applied before any parsing
	// work. Untrusted bodies above this size return errSanitizeInputTooLarge
	// without touching the parser, so a hostile sender can't burn CPU on
	// gigabytes of garbage before the post-pipeline cap rejects it.
	sanitizeMaxInputBytes = 4 << 20
	// sanitizeMaxBytes caps the post-bluemonday output size.
	sanitizeMaxBytes = 1 << 20
	// sanitizeMaxRemoteImages caps the number of remote-image URLs the
	// rewrite walk records into the handle cache. Above this we fail
	// sanitization rather than truncate so the proxy never serves a
	// partial-trust subset of a message's images.
	sanitizeMaxRemoteImages = 256
	// sanitizeMaxRemoteImageURLBytes caps the total byte length of all
	// recorded remote-image URLs combined. Defense-in-depth: keeps a few
	// long URLs from pinning large entries in the LRU cache.
	sanitizeMaxRemoteImageURLBytes = 256 * 1024
	// sanitizeMaxDOMDepth caps parsed tree nesting before any traversal work
	// touches attacker-controlled nodes.
	sanitizeMaxDOMDepth = 256
)

type SanitizeResult struct {
	HTML             string
	RemoteImageCount int
	Token            string
}

var (
	errSanitizeInputTooLarge   = errors.New("raw input too large")
	errSanitizeOutputTooLarge  = errors.New("sanitized output too large")
	errSanitizeTooManyImages   = errors.New("too many remote images")
	errSanitizeImageURLsTooBig = errors.New("remote image urls too large")
	errSanitizeDOMTooDeep      = errors.New("html tree too deep")
	errSanitizeInvariant       = errors.New("sanitizer invariant violated")
	errSanitizePanic           = errors.New("sanitizer panic recovered")
)

type Sanitizer struct {
	policy           *bluemonday.Policy
	cache            *handleCache
	tokenKey         []byte
	generation       atomic.Uint64
	inlinePathPrefix string
}

func NewSanitizer() *Sanitizer {
	return NewSanitizerForBasePath("/")
}

func NewSanitizerForBasePath(basePath string) *Sanitizer {
	return newSanitizerForBasePathWithTokenKey(basePath, newRemoteImageTokenKey())
}

func newSanitizerForBasePathWithTokenKey(basePath string, tokenKey []byte) *Sanitizer {
	prefix := inlinePathPrefix(basePath)
	key := append([]byte(nil), tokenKey...)
	if len(key) == 0 {
		key = newRemoteImageTokenKey()
	}
	return &Sanitizer{
		policy:           newMsgvaultPolicy(prefix),
		cache:            newHandleCache(1000, 30*time.Minute),
		tokenKey:         key,
		inlinePathPrefix: prefix,
	}
}

func newRemoteImageTokenKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("msgvault remote image token key: %v", err))
	}
	return key
}

func (s *Sanitizer) BumpGeneration() {
	if s == nil {
		return
	}
	s.generation.Add(1)
	s.cache.Purge()
}

func (s *Sanitizer) Generation() uint64 {
	if s == nil {
		return 0
	}
	return s.generation.Load()
}

func (s *Sanitizer) RemoteImageURLs(messageID int64, token string, generation uint64) ([]string, bool) {
	if s == nil {
		return nil, false
	}
	return s.cache.Get(messageID, token, generation)
}

// Sanitize runs the full pipeline; on any failure returns a non-nil
// error and the caller maps that to html_sanitization_failed=true on
// the response. The pipeline:
//
//  1. Raw input cap - reject huge bodies before any parser work.
//  2. html.ParseFragment with a body element context (no head injection).
//  3. Depth-first rewrite walk: reclassify <img src>, rewrite <a href>,
//     strip style/ping/on*/data-remote-image-idx and disallowed elements,
//     drop srcset/sizes/poster.
//  4. html.Render, then bluemonday.Sanitize with the policy-allowlisted shape.
//  5. Re-parse and walk the sanitized output; reject on any survivor
//     of <script>/<style>/<iframe>/<base>/event handlers/javascript: etc.
//  6. Output size cap + remote-image count/byte caps.
//  7. Token = HMAC-SHA256(secret, gen||id||orderedURLs)[:16] hex, with gen
//     snapshotted by the caller (handler) so an in-flight request that
//     pre-dated a configure rotation cannot mint a token under the new
//     generation.
//  8. cache.Set((id, token), urls, gen).
func (s *Sanitizer) Sanitize(ctx context.Context, messageID int64, raw string, generation uint64) (res SanitizeResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", errSanitizePanic, r)
			res = SanitizeResult{}
		}
	}()

	if len(raw) > sanitizeMaxInputBytes {
		return SanitizeResult{}, errSanitizeInputTooLarge
	}

	nodes, err := html.ParseFragment(strings.NewReader(raw), &html.Node{
		Type:     html.ElementNode,
		DataAtom: atom.Body,
		Data:     "body",
	})
	if err != nil {
		return SanitizeResult{}, fmt.Errorf("parse fragment: %w", err)
	}

	var urls []string
	for _, n := range nodes {
		if err := rewriteWalk(n, messageID, s.inlinePathPrefix, &urls); err != nil {
			return SanitizeResult{}, err
		}
	}

	if len(urls) > sanitizeMaxRemoteImages {
		return SanitizeResult{}, errSanitizeTooManyImages
	}
	totalURLBytes := 0
	for _, u := range urls {
		totalURLBytes += len(u)
	}
	if totalURLBytes > sanitizeMaxRemoteImageURLBytes {
		return SanitizeResult{}, errSanitizeImageURLsTooBig
	}

	var buf bytes.Buffer
	for _, n := range nodes {
		if err := html.Render(&buf, n); err != nil {
			return SanitizeResult{}, fmt.Errorf("render: %w", err)
		}
	}

	sanitized := s.policy.Sanitize(buf.String())

	if err := invariantCheck(sanitized); err != nil {
		return SanitizeResult{}, err
	}

	if len(sanitized) > sanitizeMaxBytes {
		return SanitizeResult{}, errSanitizeOutputTooLarge
	}

	token := computeToken(s.tokenKey, generation, messageID, urls)
	s.cache.Set(messageID, token, generation, urls)
	_ = ctx

	return SanitizeResult{
		HTML:             sanitized,
		RemoteImageCount: len(urls),
		Token:            token,
	}, nil
}

func computeToken(key []byte, gen uint64, messageID int64, urls []string) string {
	h := hmac.New(sha256.New, key)
	_ = binary.Write(h, binary.BigEndian, gen)
	h.Write([]byte{0})
	_ = binary.Write(h, binary.BigEndian, messageID)
	h.Write([]byte{0})
	for _, u := range urls {
		h.Write([]byte(u))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// disallowedTags is the set whose elements are dropped entirely by the
// rewrite walk. The invariant walk re-asserts these are absent after
// bluemonday runs.
var disallowedTags = map[string]struct{}{
	"script": {}, "style": {}, "svg": {}, "math": {},
	"form": {}, "input": {}, "button": {}, "select": {},
	"option": {}, "textarea": {},
	"base": {}, "meta": {}, "link": {},
	"iframe": {}, "object": {}, "embed": {},
	"audio": {}, "video": {}, "source": {}, "track": {},
	"canvas": {}, "applet": {}, "frame": {}, "frameset": {},
}

type nodeFrame struct {
	node  *html.Node
	depth int
}

func rewriteWalk(root *html.Node, messageID int64, inlinePathPrefix string, urls *[]string) error {
	stack := []nodeFrame{{node: root, depth: 1}}
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if frame.depth > sanitizeMaxDOMDepth {
			return errSanitizeDOMTooDeep
		}
		n := frame.node
		skipChildren := false
		if n.Type == html.ElementNode {
			if _, drop := disallowedTags[n.Data]; drop {
				n.Type = html.TextNode
				n.Data = ""
				n.FirstChild = nil
				n.LastChild = nil
				n.Attr = nil
				skipChildren = true
			} else {
				switch n.Data {
				case "img":
					rewriteImg(n, messageID, inlinePathPrefix, urls)
				case "a":
					rewriteAnchor(n)
				}
				n.Attr = filterAttrs(n.Attr)
			}
		}
		if skipChildren {
			continue
		}
		for c := n.LastChild; c != nil; c = c.PrevSibling {
			stack = append(stack, nodeFrame{node: c, depth: frame.depth + 1})
		}
	}
	return nil
}

func filterAttrs(attrs []html.Attribute) []html.Attribute {
	out := attrs[:0]
	for _, a := range attrs {
		key := strings.ToLower(a.Key)
		if key == "style" || key == "ping" {
			continue
		}
		if strings.HasPrefix(key, "on") {
			continue
		}
		if key == "formaction" {
			continue
		}
		out = append(out, a)
	}
	return out
}

var imgSrcAttrs = map[string]struct{}{
	"srcset": {}, "sizes": {}, "poster": {},
}

func rewriteImg(n *html.Node, messageID int64, inlinePathPrefix string, urls *[]string) {
	cleaned := n.Attr[:0]
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if _, drop := imgSrcAttrs[key]; drop {
			continue
		}
		// Strip any incoming data-remote-image-idx so untrusted senders
		// can't forge a placeholder for a URL that isn't in the recorded
		// list. The rewrite walk re-adds the attribute only for the
		// remote URLs it itself records, immediately below.
		if key == "data-remote-image-idx" {
			continue
		}
		cleaned = append(cleaned, a)
	}
	n.Attr = cleaned

	srcIdx := -1
	var src string
	for i, a := range n.Attr {
		if strings.EqualFold(a.Key, "src") {
			srcIdx = i
			src = a.Val
			break
		}
	}
	if srcIdx == -1 {
		return
	}

	classification := classifyImgSrc(src)
	switch classification.kind {
	case "cid":
		n.Attr[srcIdx].Val = fmt.Sprintf(
			"%s/msgvault/messages/%d/inline?cid=%s",
			inlinePathPrefix, messageID, url.QueryEscape(classification.value),
		)
	case "data_png", "data_jpeg", "data_gif", "data_webp":
		// keep as-is
	case "remote":
		idx := len(*urls)
		*urls = append(*urls, classification.value)
		n.Attr = append(n.Attr[:srcIdx], n.Attr[srcIdx+1:]...)
		n.Attr = append(n.Attr, html.Attribute{
			Key: "data-remote-image-idx",
			Val: fmt.Sprintf("%d", idx),
		})
	default:
		n.Attr = append(n.Attr[:srcIdx], n.Attr[srcIdx+1:]...)
	}
}

type srcClassification struct {
	kind  string
	value string
}

var dataImgRe = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/=]+$`)

func classifyImgSrc(src string) srcClassification {
	src = strings.TrimSpace(src)
	if src == "" {
		return srcClassification{kind: "empty"}
	}
	if strings.HasPrefix(strings.ToLower(src), "cid:") {
		return srcClassification{kind: "cid", value: src[4:]}
	}
	if dataImgRe.MatchString(src) {
		return srcClassification{kind: "data_png", value: src}
	}
	low := strings.ToLower(src)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return srcClassification{kind: "remote", value: src}
	}
	return srcClassification{kind: "other"}
}

var anchorSchemeRe = regexp.MustCompile(`^(?i)(mailto:|tel:|https?://)`)

func rewriteAnchor(n *html.Node) {
	keep := n.Attr[:0]
	sawHref := false
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if key == "href" {
			if anchorSchemeRe.MatchString(a.Val) {
				keep = append(keep, a)
			}
			sawHref = true
			continue
		}
		if key == "target" || key == "rel" {
			continue
		}
		keep = append(keep, a)
	}
	_ = sawHref
	keep = append(keep, html.Attribute{Key: "target", Val: "_blank"})
	keep = append(keep, html.Attribute{Key: "rel", Val: "noopener noreferrer"})
	n.Attr = keep
}

func invariantCheck(sanitized string) error {
	nodes, err := html.ParseFragment(strings.NewReader(sanitized), &html.Node{
		Type:     html.ElementNode,
		DataAtom: atom.Body,
		Data:     "body",
	})
	if err != nil {
		return fmt.Errorf("invariant reparse: %w", err)
	}
	for _, root := range nodes {
		stack := []nodeFrame{{node: root, depth: 1}}
		for len(stack) > 0 {
			frame := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if frame.depth > sanitizeMaxDOMDepth {
				return errSanitizeDOMTooDeep
			}
			n := frame.node
			if n.Type == html.ElementNode {
				if _, drop := disallowedTags[n.Data]; drop {
					return fmt.Errorf("%w: tag %s survived", errSanitizeInvariant, n.Data)
				}
				for _, a := range n.Attr {
					key := strings.ToLower(a.Key)
					if key == "style" || key == "ping" || strings.HasPrefix(key, "on") {
						return fmt.Errorf("%w: attr %s survived", errSanitizeInvariant, key)
					}
					if key == "href" || key == "src" || key == "action" || key == "formaction" {
						if !safeInvariantURL(a.Val) {
							return fmt.Errorf("%w: dangerous scheme in %s", errSanitizeInvariant, key)
						}
					}
				}
			}
			for c := n.LastChild; c != nil; c = c.PrevSibling {
				stack = append(stack, nodeFrame{node: c, depth: frame.depth + 1})
			}
		}
	}
	return nil
}

// invariantSchemeRe matches a URL scheme token at the start of a
// normalized attribute value.
var invariantSchemeRe = regexp.MustCompile(`^[a-z][a-z0-9+.\-]*:`)

// safeInvariantURL reports whether a URL attribute that survived
// sanitization is one of the shapes the sanitizer can legitimately emit:
// scheme-less relative/path URLs, mailto:, tel:, http(s)://, or the
// strict base64 image data URLs the rewrite pass allows. Anything else
// (javascript:, vbscript:, data:text/html, blob:, file:, unknown remote
// helpers) fails the invariant. The value is normalized the way browsers
// preprocess URLs — ASCII tab/LF/CR stripped anywhere, C0 controls and
// spaces trimmed at the ends — so an obfuscated scheme such as
// "jav\tascript:" cannot masquerade as scheme-less.
func safeInvariantURL(raw string) bool {
	v := strings.ToLower(raw)
	v = strings.TrimFunc(v, func(r rune) bool { return r <= ' ' })
	v = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(v)
	scheme := invariantSchemeRe.FindString(v)
	if scheme == "" {
		// A scheme-less value that opens with two slash-like bytes is a
		// protocol-relative network reference (//host/x, and the
		// backslash variants browsers fold to //host) — it loads from the
		// network with the page's scheme. The sanitizer never emits one,
		// so reject; legitimate scheme-less values are the empty string
		// and single-slash local paths.
		if len(v) >= 2 && isSlashByte(v[0]) && isSlashByte(v[1]) {
			return false
		}
		return true
	}
	switch strings.TrimSuffix(scheme, ":") {
	case "http", "https", "mailto", "tel":
		return true
	case "data":
		return dataImgRe.MatchString(v)
	default:
		return false
	}
}

func isSlashByte(b byte) bool {
	return b == '/' || b == '\\'
}

func inlinePathPrefix(basePath string) string {
	base := strings.TrimSpace(basePath)
	if base == "" || base == "/" {
		return "/api/v1"
	}
	return strings.TrimRight(base, "/") + "/api/v1"
}

func newMsgvaultPolicy(inlinePathPrefix string) *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "div", "span", "br", "hr",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"ul", "ol", "li", "blockquote",
		"table", "thead", "tbody", "tfoot", "tr", "td", "th",
		"b", "strong", "i", "em", "u", "s", "code", "pre",
		"a", "img")
	p.AllowAttrs("href").Matching(regexp.MustCompile(`^(?i)(mailto:|tel:|https?://)`)).OnElements("a")
	p.AllowAttrs("target").Matching(regexp.MustCompile(`^_blank$`)).OnElements("a")
	p.AllowAttrs("rel").Matching(regexp.MustCompile(`^noopener noreferrer$`)).OnElements("a")
	p.AllowAttrs("src").Matching(regexp.MustCompile(
		`^$|^` + regexp.QuoteMeta(inlinePathPrefix) + `/msgvault/messages/\d+/inline\?cid=|^data:image/(png|jpeg|gif|webp);base64,[A-Za-z0-9+/=]+$`,
	)).OnElements("img")
	p.AllowAttrs("data-remote-image-idx").Matching(regexp.MustCompile(`^\d+$`)).OnElements("img")
	p.AllowAttrs("alt").OnElements("img")
	p.AllowAttrs("width", "height").Matching(regexp.MustCompile(`^\d+$`)).OnElements("img")
	p.AllowAttrs("colspan", "rowspan").Matching(regexp.MustCompile(`^\d+$`)).OnElements("td", "th")
	p.AllowAttrs("align").Matching(regexp.MustCompile(`^(left|right|center|justify)$`)).OnElements("td", "th", "p", "div")
	return p
}
