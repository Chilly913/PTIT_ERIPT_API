package httpx

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// PageParams are the parsed `page` / `per_page` query parameters.
type PageParams struct {
	Page    int
	PerPage int
}

// Offset is the SQL OFFSET for the page.
func (p PageParams) Offset() int { return (p.Page - 1) * p.PerPage }

// Limit is the SQL LIMIT for the page.
func (p PageParams) Limit() int { return p.PerPage }

// ParsePage reads `page` (Laravel: any integer >= 1, else 1) and `per_page`
// (missing/invalid/<1 -> defPerPage, capped at maxPerPage).
func ParsePage(c *gin.Context, defPerPage, maxPerPage int) PageParams {
	page, err := strconv.Atoi(c.Query("page"))
	if err != nil || page < 1 {
		page = 1
	}
	per, err := strconv.Atoi(c.Query("per_page"))
	if err != nil || per < 1 {
		per = defPerPage
	}
	if maxPerPage > 0 && per > maxPerPage {
		per = maxPerPage
	}
	return PageParams{Page: page, PerPage: per}
}

// PageLink is one entry of the paginator "links" array.
type PageLink struct {
	URL    *string `json:"url"`
	Label  string  `json:"label"`
	Page   *int    `json:"page"`
	Active bool    `json:"active"`
}

// Page is the JSON shape of Laravel's LengthAwarePaginator::toArray().
type Page[T any] struct {
	CurrentPage  int        `json:"current_page"`
	Data         []T        `json:"data"`
	FirstPageURL string     `json:"first_page_url"`
	From         *int       `json:"from"`
	LastPage     int        `json:"last_page"`
	LastPageURL  string     `json:"last_page_url"`
	Links        []PageLink `json:"links"`
	NextPageURL  *string    `json:"next_page_url"`
	Path         string     `json:"path"`
	PerPage      int        `json:"per_page"`
	PrevPageURL  *string    `json:"prev_page_url"`
	To           *int       `json:"to"`
	Total        int64      `json:"total"`
}

// NewPage builds a paginator for path (scheme://host/path, no query string).
func NewPage[T any](path string, items []T, total int64, p PageParams) Page[T] {
	if items == nil {
		items = []T{}
	}
	if p.PerPage < 1 {
		p.PerPage = 1
	}
	if p.Page < 1 {
		p.Page = 1
	}
	lastPage := int((total + int64(p.PerPage) - 1) / int64(p.PerPage))
	if lastPage < 1 {
		lastPage = 1
	}
	url := func(page int) string {
		if page < 1 {
			page = 1
		}
		return path + "?page=" + strconv.Itoa(page)
	}
	pg := Page[T]{
		CurrentPage:  p.Page,
		Data:         items,
		FirstPageURL: url(1),
		LastPage:     lastPage,
		LastPageURL:  url(lastPage),
		Path:         path,
		PerPage:      p.PerPage,
		Total:        total,
	}
	if len(items) > 0 {
		from := (p.Page-1)*p.PerPage + 1
		to := from + len(items) - 1
		pg.From, pg.To = &from, &to
	}
	if p.Page < lastPage {
		s := url(p.Page + 1)
		pg.NextPageURL = &s
	}
	if p.Page > 1 {
		s := url(p.Page - 1)
		pg.PrevPageURL = &s
	}
	pg.Links = pageLinks(p.Page, lastPage, url, pg.PrevPageURL, pg.NextPageURL)
	return pg
}

const onEachSide = 3 // Laravel AbstractPaginator::$onEachSide

// pageLinks reproduces LengthAwarePaginator::linkCollection() + UrlWindow.
func pageLinks(current, last int, url func(int) string, prev, next *string) []PageLink {
	rng := func(from, to int) []int {
		var out []int
		for i := from; i <= to; i++ {
			out = append(out, i)
		}
		return out
	}
	var first, slider, end []int
	window := onEachSide + 4
	switch {
	case last < onEachSide*2+8:
		first = rng(1, last)
	case current <= window:
		first, end = rng(1, window+onEachSide), rng(last-1, last)
	case current > last-window:
		first, end = rng(1, 2), rng(last-(window+(onEachSide-1)), last)
	default:
		first, slider, end = rng(1, 2), rng(current-onEachSide, current+onEachSide), rng(last-1, last)
	}

	var prevPage, nextPage *int
	if current > 1 {
		v := current - 1
		prevPage = &v
	}
	if current < last {
		v := current + 1
		nextPage = &v
	}
	links := []PageLink{{URL: prev, Label: "&laquo; Previous", Page: prevPage}}
	addPages := func(pages []int) {
		for _, n := range pages {
			u := url(n)
			n := n
			links = append(links, PageLink{URL: &u, Label: strconv.Itoa(n), Page: &n, Active: n == current})
		}
	}
	dots := PageLink{Label: "..."}
	addPages(first)
	if slider != nil {
		links = append(links, dots)
		addPages(slider)
	}
	if end != nil {
		links = append(links, dots)
		addPages(end)
	}
	return append(links, PageLink{URL: next, Label: "Next &raquo;", Page: nextPage})
}

// RequestPath returns scheme://host/path of the current request (Laravel's
// $request->url()), honouring X-Forwarded-Proto/Host set by the proxy.
func RequestPath(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if p := firstValue(c.GetHeader("X-Forwarded-Proto")); p == "https" || p == "http" {
		scheme = p
	}
	host := c.Request.Host
	if h := firstValue(c.GetHeader("X-Forwarded-Host")); h != "" {
		host = h
	}
	return scheme + "://" + host + c.Request.URL.Path
}

func firstValue(h string) string {
	if i := strings.IndexByte(h, ','); i >= 0 {
		h = h[:i]
	}
	return strings.TrimSpace(h)
}

// Paginate renders a Laravel paginator for the current request with 200.
func Paginate[T any](c *gin.Context, items []T, total int64, p PageParams) {
	c.JSON(http.StatusOK, NewPage(RequestPath(c), items, total, p))
}
