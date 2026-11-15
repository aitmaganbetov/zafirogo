package httpserver

import (
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Сжатие ответов в самом сервере (PB-11, docs/open-questions/audit-perf.md): один бинарник должен отдавать JSON
// компактно и без nginx перед ним. Вшитый фронт сжимает себя сам (webui: один раз на файл, Content-Encoding уже
// стоит — здесь такие ответы не трогаются).
//
// Правила:
//   - только клиентам с Accept-Encoding: gzip; только текстовые типы (JSON, text/*, JS, SVG, XML);
//   - ответы меньше compressMin байт, уже сжатые (Content-Encoding), 1xx/204/304, HEAD — как есть;
//   - WebSocket (заголовок Upgrade) и потоки (text/event-stream, Flush до набора compressMin) — как есть;
//   - /api/auth/* — как есть: в ответах токены (сжатие секрета вместе с управляемым извне текстом — BREACH);
//   - сильный ETag обработчика становится слабым (W/): тело на проводе другое; Vary: Accept-Encoding.
const (
	compressMin   = 1024
	compressLevel = 5
)

var gzipWriters = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(io.Discard, compressLevel)
	return zw
}}

// compress — middleware сжатия ответов.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Upgrade") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) ||
			strings.HasPrefix(r.URL.Path, "/api/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w}
		next.ServeHTTP(cw, r)
		cw.finish() // при панике обработчика не вызывается: недописанный буфер не уходит перед ответом Recoverer
	})
}

// acceptsGzip — в Accept-Encoding есть gzip без q=0.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			continue
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f == 0 {
				return false
			}
		}
		return true
	}
	return false
}

// compressibleType — текстовые типы ответов API.
func compressibleType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch {
	case mt == "text/event-stream":
		return false
	case strings.HasPrefix(mt, "text/"), mt == "application/json", strings.HasSuffix(mt, "+json"),
		mt == "application/javascript", mt == "application/xml", strings.HasSuffix(mt, "+xml"):
		return true
	}
	return false
}

// compressWriter копит начало ответа, пока не ясно, сжимать ли его (тип и размер), затем либо сжимает, либо
// пропускает как есть.
type compressWriter struct {
	http.ResponseWriter
	status int
	buf    []byte
	zw     *gzip.Writer
	// plain — решено не сжимать: всё идёт в исходный ResponseWriter.
	plain bool
}

// Unwrap — для http.ResponseController (дедлайны записи и т. п.).
func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *compressWriter) WriteHeader(code int) {
	if c.plain || c.zw != nil || c.status != 0 {
		if c.plain {
			c.ResponseWriter.WriteHeader(code)
		}
		return
	}
	if code < 200 { // 1xx — информационные, решение — по финальному статусу
		c.ResponseWriter.WriteHeader(code)
		return
	}
	c.status = code
	h := c.Header()
	if code == http.StatusNoContent || code == http.StatusNotModified || h.Get("Content-Encoding") != "" ||
		(h.Get("Content-Type") != "" && !compressibleType(h.Get("Content-Type"))) {
		_ = c.passthrough() // буфер пуст: только статус
		return
	}
	if n, err := strconv.Atoi(h.Get("Content-Length")); err == nil && n < compressMin {
		_ = c.passthrough()
	}
}

func (c *compressWriter) Write(b []byte) (int, error) {
	switch {
	case c.plain:
		return c.ResponseWriter.Write(b)
	case c.zw != nil:
		return c.zw.Write(b)
	}
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
		if c.plain {
			return c.ResponseWriter.Write(b)
		}
	}
	c.buf = append(c.buf, b...)
	if len(c.buf) < compressMin {
		return len(b), nil
	}
	h := c.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", http.DetectContentType(c.buf))
	}
	if h.Get("Content-Encoding") != "" || !compressibleType(h.Get("Content-Type")) {
		if err := c.passthrough(); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	addVary(h)
	if tag := h.Get("ETag"); tag != "" && !strings.HasPrefix(tag, "W/") {
		h.Set("ETag", "W/"+tag)
	}
	c.ResponseWriter.WriteHeader(c.status)
	zw, ok := gzipWriters.Get().(*gzip.Writer)
	if !ok {
		zw, _ = gzip.NewWriterLevel(io.Discard, compressLevel)
	}
	zw.Reset(c.ResponseWriter)
	c.zw = zw
	buf := c.buf
	c.buf = nil
	if _, err := zw.Write(buf); err != nil {
		return 0, err
	}
	return len(b), nil
}

// passthrough — не сжимать: отдать статус и накопленное как есть.
func (c *compressWriter) passthrough() error {
	c.plain = true
	if compressibleType(c.Header().Get("Content-Type")) {
		addVary(c.Header()) // сжатый вариант этого же ресурса возможен
	}
	if c.status != 0 {
		c.ResponseWriter.WriteHeader(c.status)
	}
	if len(c.buf) == 0 {
		return nil
	}
	buf := c.buf
	c.buf = nil
	_, err := c.ResponseWriter.Write(buf)
	return err
}

// Flush — поток: если сжатие ещё не началось, оно и не начнётся (клиент ждёт данные сейчас).
func (c *compressWriter) Flush() {
	if c.zw != nil {
		_ = c.zw.Flush()
	} else if !c.plain {
		if c.status == 0 {
			c.status = http.StatusOK
		}
		_ = c.passthrough()
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// finish — конец ответа: закрыть поток gzip либо отдать маленький ответ как есть.
func (c *compressWriter) finish() {
	switch {
	case c.zw != nil:
		_ = c.zw.Close()
		c.zw.Reset(io.Discard)
		gzipWriters.Put(c.zw)
		c.zw = nil
	case !c.plain && c.status != 0:
		_ = c.passthrough()
	}
}

func addVary(h http.Header) {
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(f), "Accept-Encoding") {
				return
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
}
