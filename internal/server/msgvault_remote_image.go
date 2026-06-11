package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	msgvaultRemoteImageMaxBytes     = 5 << 20
	msgvaultRemoteImageMaxRedirects = 3
	msgvaultRemoteImageDialTimeout  = 5 * time.Second
	msgvaultRemoteImageRespTimeout  = 10 * time.Second
	msgvaultRemoteImageTotalTimeout = 15 * time.Second
)

var (
	errMsgvaultRemoteImageBadScheme = errors.New("scheme not allowed")
	errMsgvaultRemoteImageUserinfo  = errors.New("userinfo in url")
	errMsgvaultRemoteImageZoneID    = errors.New("zone id in host")
	errMsgvaultRemoteImageBadPort   = errors.New("port not allowed")
	errMsgvaultRemoteImagePrivateIP = errors.New("private or reserved address")
	errMsgvaultRemoteImageNoIPs     = errors.New("no addresses resolved")
)

type msgvaultRemoteImageLookupFunc func(ctx context.Context, host string) ([]netip.Addr, error)
type msgvaultRemoteImageDialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type msgvaultRemoteImageDeps struct {
	lookup msgvaultRemoteImageLookupFunc
	dial   msgvaultRemoteImageDialFunc
}

func defaultMsgvaultRemoteImageDeps() msgvaultRemoteImageDeps {
	return msgvaultRemoteImageDeps{
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		dial: (&net.Dialer{Timeout: msgvaultRemoteImageDialTimeout}).DialContext,
	}
}

func (d msgvaultRemoteImageDeps) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := d.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errMsgvaultRemoteImageNoIPs
	}
	if slices.ContainsFunc(ips, isMsgvaultRemoteImagePrivateOrReserved) {
		return nil, errMsgvaultRemoteImagePrivateIP
	}
	var lastErr error
	for _, ip := range ips {
		conn, err := d.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func validateMsgvaultRemoteImageURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return errMsgvaultRemoteImageBadScheme
	}
	if u.User != nil {
		return errMsgvaultRemoteImageUserinfo
	}
	if strings.ContainsRune(u.Hostname(), '%') {
		return errMsgvaultRemoteImageZoneID
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	if port != "80" && port != "443" {
		return errMsgvaultRemoteImageBadPort
	}
	return nil
}

var msgvaultRemoteImagePrivatePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("::ffff:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isMsgvaultRemoteImagePrivateOrReserved(ip netip.Addr) bool {
	if ip.Is4In6() && isMsgvaultRemoteImagePrivateOrReserved(ip.Unmap()) {
		return true
	}
	for _, prefix := range msgvaultRemoteImagePrivatePrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

var msgvaultRemoteImageAllowedMIME = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
}

func (h *msgvaultHandler) remoteImage(ctx context.Context, in *msgvaultRemoteImageInput) (*msgvaultRawImageOutput, error) {
	if _, prob := h.requireConfigured(); prob != nil {
		return nil, prob
	}
	if in.Token == "" || in.Idx < 0 {
		return nil, msgvaultImageRejectedProblem()
	}

	gen := h.sanitizerGeneration()
	urls, ok := h.remoteImageURLs(in.ID, in.Token, gen)
	if !ok {
		var status msgvaultRemoteImageRefetchStatus
		urls, status = h.refetchAndResanitize(ctx, in.ID, in.Token)
		switch status {
		case msgvaultRemoteImageRefetchOK:
		case msgvaultRemoteImageRefetchTokenMismatch:
			return nil, msgvaultImageNotFoundProblem()
		default:
			return nil, msgvaultImageFetchFailedProblem()
		}
	}
	if in.Idx >= len(urls) {
		return nil, msgvaultImageNotFoundProblem()
	}
	imageURL := urls[in.Idx]
	u, parseErr := url.Parse(imageURL)
	if parseErr != nil || validateMsgvaultRemoteImageURL(u) != nil {
		return nil, msgvaultImageRejectedProblem()
	}

	client := h.remoteImageHTTPClient()
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, msgvaultImageFetchFailedProblem()
	}
	req.Header.Set("User-Agent", "middleman-msgvault/1")

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, nil
		}
		return nil, msgvaultImageFetchFailedProblem()
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, msgvaultImageFetchFailedProblem()
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") {
		return nil, msgvaultImageFetchFailedProblem()
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if _, ok := msgvaultRemoteImageAllowedMIME[mediaType]; !ok {
		return nil, newProblem(
			http.StatusUnsupportedMediaType,
			CodeBadRequest,
			"unsupported image type",
			map[string]any{"reason": "unsupportedImageType"},
		)
	}

	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(resp.Body, msgvaultRemoteImageMaxBytes+1))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, nil
		}
		return nil, msgvaultImageFetchFailedProblem()
	}
	if n > msgvaultRemoteImageMaxBytes {
		return nil, msgvaultImageFetchFailedProblem()
	}
	return &msgvaultRawImageOutput{
		ContentType:        mediaType,
		ContentTypeOptions: "nosniff",
		CacheControl:       "private, max-age=300",
		ContentLength:      strconv.Itoa(buf.Len()),
		Body:               buf.Bytes(),
	}, nil
}

func (h *msgvaultHandler) remoteImageHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		DialContext:           h.remoteDeps.dialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: msgvaultRemoteImageRespTimeout,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   msgvaultRemoteImageTotalTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Referer")
			if len(via) > msgvaultRemoteImageMaxRedirects {
				return errors.New("too many redirects")
			}
			return validateMsgvaultRemoteImageURL(req.URL)
		},
	}
}

func (h *msgvaultHandler) remoteImageURLs(messageID int64, token string, generation uint64) ([]string, bool) {
	h.mu.Lock()
	sanitizer := h.sanitizer
	h.mu.Unlock()
	return sanitizer.RemoteImageURLs(messageID, token, generation)
}

type msgvaultRemoteImageRefetchStatus int

const (
	msgvaultRemoteImageRefetchOK msgvaultRemoteImageRefetchStatus = iota
	msgvaultRemoteImageRefetchTokenMismatch
	msgvaultRemoteImageRefetchUpstreamErr
)

func (h *msgvaultHandler) refetchAndResanitize(ctx context.Context, messageID int64, requestedToken string) ([]string, msgvaultRemoteImageRefetchStatus) {
	client, sanitizer, gen, prob := h.requireConfiguredWithSanitizer()
	if prob != nil {
		return nil, msgvaultRemoteImageRefetchUpstreamErr
	}
	detail, err := client.Message(ctx, messageID)
	if err != nil {
		return nil, msgvaultRemoteImageRefetchUpstreamErr
	}
	if detail == nil {
		return nil, msgvaultRemoteImageRefetchUpstreamErr
	}
	res, sanErr := sanitizer.Sanitize(ctx, messageID, detail.BodyHTML, gen)
	if sanErr != nil {
		return nil, msgvaultRemoteImageRefetchUpstreamErr
	}
	if res.Token != requestedToken {
		return nil, msgvaultRemoteImageRefetchTokenMismatch
	}
	urls, ok := sanitizer.RemoteImageURLs(messageID, res.Token, gen)
	if !ok {
		return nil, msgvaultRemoteImageRefetchUpstreamErr
	}
	return urls, msgvaultRemoteImageRefetchOK
}

func msgvaultImageRejectedProblem() error {
	return problemBadRequest(
		CodeBadRequest,
		"image rejected",
		map[string]any{"reason": "imageRejected"},
	)
}

func msgvaultImageNotFoundProblem() error {
	return problemNotFound(
		CodeNotFound,
		"image not found",
		map[string]any{"reason": "imageNotFound"},
	)
}

func msgvaultImageFetchFailedProblem() error {
	return newProblem(
		http.StatusBadGateway,
		CodeUpstreamError,
		"image fetch failed",
		map[string]any{"reason": "imageFetchFailed"},
	)
}
