package main

import (
	"bytes"
	"crypto/md5"
	"encoding/xml"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	text_template "text/template"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
	"air"
	"github.com/tdewolff/minify"
	minify_xml "github.com/tdewolff/minify/xml"
	"gopkg.in/russross/blackfriday.v2"
)

type post struct {
	ID       string
	Title    string
	Datetime time.Time
	Content  template.HTML
}

var (
	once sync.Once

	posts        map[string]post
	orderedPosts []post

	feed             []byte
	feedTemplate     *text_template.Template
	feedETag         string
	feedLastModified string
)

func main() {
	b, err := ioutil.ReadFile(filepath.Join(air.TemplateRoot, "feed.xml"))
	if err != nil {
		panic(err)
	}

	feedTemplate = text_template.Must(
		text_template.New("feed").Funcs(map[string]interface{}{
			"xmlescape": func(s string) string {
				buf := &bytes.Buffer{}
				xml.Escape(buf, []byte(s))
				return buf.String()
			},
			"now": func() time.Time {
				return time.Now().UTC()
			},
			"timefmt": air.TemplateFuncMap["timefmt"],
		}).Parse(string(b)),
	)

	go func() {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			panic(err)
		}

		go func() {
			for {
				select {
				case event := <-watcher.Events:
					air.INFO(event)
					once = sync.Once{}
				case err := <-watcher.Errors:
					air.ERROR(err)
				}
			}
		}()

		if err := watcher.Add("posts"); err != nil {
			panic(err)
		}
	}()

	air.Gases = []air.Gas{
		baseGas,
	}

	air.TemplateFuncMap["sub"] = func(a, b int) int { return a - b }

	air.STATIC(
		"/assets",
		air.AssetRoot,
		air.WrapGas(func(req *air.Request, res *air.Response) error {
			res.Headers["Cache-Control"] = "max-age=3600"
			return nil
		}),
	)
	air.GET("/", homeHandler)
	air.HEAD("/", homeHandler)
	air.GET("/posts", postsHandler)
	air.HEAD("/posts", postsHandler)
	air.GET("/posts/:ID", postHandler)
	air.HEAD("/posts/:ID", postHandler)
	air.GET("/bio", bioHandler)
	air.HEAD("/bio", bioHandler)
	air.GET("/feed", feedHandler)
	air.HEAD("/feed", feedHandler)

	air.ErrorHandler = errorHandler

	shutdownChan := make(chan os.Signal)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := air.Serve(); err != nil {
			air.ERROR(err)
		}
	}()

	<-shutdownChan
	air.Shutdown(0)
}

func parsePosts() {
	fns, _ := filepath.Glob("posts/*.md")
	newPosts := make(map[string]post, len(fns))
	newOrderedPosts := make([]post, 0, len(fns))
	for _, fn := range fns {
		b, err := ioutil.ReadFile(fn)
		if err != nil {
			continue
		}

		s := string(b)
		if strings.Count(s, "+++") < 2 {
			continue
		}

		i := strings.Index(s, "+++")
		j := strings.Index(s[i+3:], "+++") + 3

		p := post{
			ID: fn[6 : len(fn)-3],
			Content: template.HTML(
				blackfriday.Run([]byte(s[j+3:])),
			),
		}
		if err := toml.Unmarshal([]byte(s[i+3:j]), &p); err != nil {
			continue
		}

		p.Datetime = p.Datetime.UTC()

		newPosts[p.ID] = p
		newOrderedPosts = append(newOrderedPosts, p)
	}

	sort.Slice(newOrderedPosts, func(i, j int) bool {
		return newOrderedPosts[i].Datetime.After(
			newOrderedPosts[j].Datetime,
		)
	})

	posts = newPosts
	orderedPosts = newOrderedPosts

	latestPosts := orderedPosts
	if len(latestPosts) > 10 {
		latestPosts = latestPosts[:10]
	}

	buf := &bytes.Buffer{}
	feedTemplate.Execute(buf, map[string]interface{}{
		"Config": air.Config,
		"Posts":  latestPosts,
	})

	buf2 := &bytes.Buffer{}
	minify_xml.DefaultMinifier.Minify(minify.New(), buf2, buf, nil)

	if b := buf2.Bytes(); !bytes.Equal(b, feed) {
		feed = b
		feedETag = fmt.Sprintf(`"%x"`, md5.Sum(feed))
		feedLastModified = time.Now().UTC().Format(http.TimeFormat)
	}
}

func baseGas(next air.Handler) air.Handler {
	return func(req *air.Request, res *air.Response) error {
		req.Values["Config"] = air.Config
		if req.Method == "GET" || req.Method == "HEAD" {
			res.Headers["Cache-Control"] = "no-cache"
		}
		return next(req, res)
	}
}

func homeHandler(req *air.Request, res *air.Response) error {
	req.Values["CanonicalPath"] = ""
	return res.Render(req.Values, "index.html")
}

func postsHandler(req *air.Request, res *air.Response) error {
	once.Do(parsePosts)
	req.Values["PageTitle"] = "Posts"
	req.Values["CanonicalPath"] = "/posts"
	req.Values["IsPosts"] = true
	req.Values["Posts"] = orderedPosts
	return res.Render(req.Values, "posts.html", "layouts/default.html")
}

func postHandler(req *air.Request, res *air.Response) error {
	once.Do(parsePosts)
	p, ok := posts[req.Params["ID"]]
	if !ok {
		return air.NotFoundHandler(req, res)
	}
	req.Values["PageTitle"] = p.Title
	req.Values["CanonicalPath"] = "/posts/" + p.ID
	req.Values["IsPosts"] = true
	req.Values["Post"] = p
	return res.Render(req.Values, "post.html", "layouts/default.html")
}

func bioHandler(req *air.Request, res *air.Response) error {
	req.Values["PageTitle"] = "Bio"
	req.Values["CanonicalPath"] = "/bio"
	req.Values["IsBio"] = true
	return res.Render(req.Values, "bio.html", "layouts/default.html")
}

func feedHandler(req *air.Request, res *air.Response) error {
	once.Do(parsePosts)
	res.Headers["Content-Type"] = "application/atom+xml; charset=utf-8"
	res.Headers["Cache-Control"] = "max-age=3600"
	res.Headers["ETag"] = feedETag
	res.Headers["Last-Modified"] = feedLastModified
	return res.Blob(feed)
}

func errorHandler(err error, req *air.Request, res *air.Response) {
	e := &air.Error{500, "Internal Server Error"}
	if ce, ok := err.(*air.Error); ok {
		e = ce
	} else if air.DebugMode {
		e.Message = err.Error()
	}
	if !res.Written {
		res.StatusCode = e.Code
		if req.Method == "GET" || req.Method == "HEAD" {
			res.Headers["Cache-Control"] = "no-cache"
			delete(res.Headers, "ETag")
			delete(res.Headers, "Last-Modified")
		}
		req.Values["PageTitle"] = e.Code
		req.Values["Error"] = e
		res.Render(req.Values, "error.html", "layouts/default.html")
	}
}
