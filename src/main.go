package main

import (
	"net/http"
	"net/url"
	"fmt"
	"sync"
	"golang.org/x/net/html"
	"log"
	"github.com/mediocregopher/radix.v2/pool"
	"os"
	"path/filepath"
	"path"
	"strings"
	"github.com/tdewolff/minify"
	"github.com/tdewolff/minify/css"
	htmlMin "github.com/tdewolff/minify/html"
	"github.com/gorilla/css/scanner"
	"io"
	"bytes"
	"bufio"
	"io/ioutil"
	"time"
)

var wg sync.WaitGroup
var urls chan string

type Config struct{
	domain string
	proto string
	redisPool *pool.Pool
}

func (c Config) FullPath() string {
	return fmt.Sprintf("%s://%s", c.proto, c.domain)
}

func main() {
	urls = make(chan string, 1000)

	redisPool, err := pool.New("tcp", "localhost:6379", 10)
	checkErr("Failed to connect to Redis", err)
	cfg := Config{
		domain:"dev-portable.pantheonsite.io",
		proto:"http",
		redisPool: redisPool,
	}

	//conn,_ := redisPool.Get()
	//conn.Cmd("FLUSHALL")

	urls <- cfg.FullPath()

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			timeout := make(chan bool, 1)
			go func() {
				time.Sleep(360 * time.Second)
				timeout <- true
			}()
			fmt.Println("URL Getter")
			for true {
				select {
				case geturl := <-urls:
				// a read from ch has occurred
					fmt.Printf("Get: %s\n", geturl)
					getPage(cfg, geturl, false)

				case <-timeout:
				// the read from ch has timed out
					fmt.Printf("Timed out")
					os.Exit(0)
				}
			}
			wg.Done()
		}()
	}

	wg.Wait()
}

func checkErr(msg string, e error) {
	if e != nil {
		fmt.Printf("Error %s: %v", msg, e)
	}
}

type parentChild struct {
	parent *html.Node
	child  *html.Node
}

func getPage(cfg Config, geturl string, noCache bool) {

	if noCache != true {
		redis, _ := cfg.redisPool.Get()
		urlResp := redis.Cmd("GET", geturl)
		if urlResp.Err != nil {
			fmt.Println("Redis Error")
		} else {
			str, _ := urlResp.Str()
			fmt.Printf("Redis(%s): %v\n", geturl, str)
			if str == "1" {
				fmt.Println("Cached")

				return
			} else {
				//fmt.Println("Redis: "+urlResp.String())
			}

		}
	}

	client := &http.Client{
		CheckRedirect:func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(geturl)
	if err != nil {
		defer resp.Body.Close()
		println(resp.Status)
		return
	} else {
		if (resp.StatusCode >= 400) {
			fmt.Printf("Failed %s: %s",resp.Status,geturl)
			return
		}


		if (resp.StatusCode == 301) {
			newUrl := resp.Header.Get("location");
			urls <- newUrl
			return
		}

		data := "1"
		redis,_ := cfg.redisPool.Get()
		if putResp := redis.Cmd("SET", geturl, data); putResp.Err != nil {
			fmt.Printf("Redis error: %v", putResp.Err)
		}

		ct := resp.Header.Get("Content-Type")



		if ct == "text/css" {
			handleCSS(cfg, geturl, resp.Body)
			resp.Body.Close()
			return
		} else if ct == "text/html; charset=UTF-8" {
			handleHTML(cfg, geturl, resp.Body)
			resp.Body.Close()
			return
		} else {
			of := getOutputFile(geturl)

			io.Copy(of, resp.Body)

			resp.Body.Close()
			return
		}
		resp.Body.Close()
	}
}

func handleHTML(cfg Config, geturl string, body io.ReadCloser) {
	doc, err := html.Parse(body)
	if err != nil {
		log.Printf("Parse Err: %v\n", err)
		return
	}
	var desendants []string;
	var f func(*html.Node, []string)

	baseUrl := ""

	var cleanUp []parentChild

	dropLinks := []string{
		"pingback",
		"https://api.w.org/",
		"EditURI",
		"wlwmanifest",
		"prev",
		"shortlink",
	}
	dropAltTypes := []string{
		"application/json+oembed",
		"text/xml+oembed",
	}
	dropMeta := []string{
		"generator",
	}


	f = func(n *html.Node, desendants []string) {
		//fmt.Printf("Node %d: %v\n", depth, n.Data)
		if n.Type == html.ElementNode && n.Data == "base" {
			baseUrl = getAttr(n.Attr, "href")
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			aUrl,updatedAttr := getAbsoluteURL(cfg, geturl, getAttr(n.Attr, "href"))
			setAttr(n, n.Attr, "href", updatedAttr)
			if len(aUrl) > 0 {
				fmt.Println("Follow Links: " + aUrl)
				urls <- aUrl
			}
		}
		if n.Type == html.ElementNode && n.Data == "form" {
			_,updatedAttr := getAbsoluteURL(cfg, geturl, getAttr(n.Attr, "action"))
			setAttr(n, n.Attr, "action", updatedAttr)
		}
		if n.Type == html.ElementNode && n.Data == "meta" {
			fmt.Printf("Meta: %s\n", getAttr(n.Attr, "name"));
			if inArray(getAttr(n.Attr, "name"), dropMeta) {
				cleanUp = append(cleanUp, parentChild{
					parent:n.Parent,
					child:n,
				})
			}
		}
		if n.Type == html.ElementNode && n.Data == "link" {
			fmt.Printf("Link Rel: %s\n", getAttr(n.Attr, "rel"));
			if inArray(getAttr(n.Attr, "rel"), dropLinks) {
				cleanUp = append(cleanUp, parentChild{
					parent:n.Parent,
					child:n,
				})
			}
			if getAttr(n.Attr, "rel") == "alternate" && inArray(getAttr(n.Attr, "type"), dropAltTypes) {
				cleanUp = append(cleanUp, parentChild{
					parent:n.Parent,
					child:n,
				})
			}
			if "stylesheet" == getAttr(n.Attr, "rel") {
				cssUrl,updatedAttr := getAbsoluteURL(cfg, geturl, getAttr(n.Attr, "href"))
				setAttr(n, n.Attr, "href", updatedAttr)
				if len(cssUrl) > 0 {
					fmt.Println("Get CSS: " + cssUrl)
					urls <- cssUrl

				}
			} else {
				_,updatedAttr := getAbsoluteURL(cfg, geturl, getAttr(n.Attr, "href"))
				setAttr(n, n.Attr, "href", updatedAttr)
			}
		}
		if n.Type == html.ElementNode && n.Data == "img" {
			imgUrl,updatedAttr := getAbsoluteURL(cfg, geturl, getAttr(n.Attr, "src"))
			setAttr(n, n.Attr, "src", updatedAttr);
			if len(imgUrl) > 0 {
				fmt.Println("Get Img: " + imgUrl)
				urls <- imgUrl
			}

			scrset := strings.Split(getAttr(n.Attr, "srcset"), ",")
			var updatedSet []string
			for _,src := range scrset {

				srcBits := strings.Split(strings.Trim(src," "), " ")

				imgUrl,updatedAttr := getAbsoluteURL(cfg, geturl, srcBits[0])
				if len(srcBits) > 1 {
					updatedSet = append(updatedSet, updatedAttr+" "+srcBits[1])
				} else {
					updatedSet = append(updatedSet, updatedAttr)
				}
				if len(imgUrl) > 0 {
					fmt.Println("Get Img: " + imgUrl)
					urls <- imgUrl
				}
			}
			setAttr(n, n.Attr, "srcset", strings.Join(updatedSet, ","));
		}
		if n.Type == html.ElementNode && n.Data == "script" {
			scriptUrl,updatedAttr := getAbsoluteURL(cfg, geturl, getAttr(n.Attr, "src"))
			setAttr(n, n.Attr, "src", updatedAttr);
			if len(scriptUrl) > 0 {
				fmt.Println("Get Script: " + scriptUrl)
				urls <- scriptUrl
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			//fmt.Printf("tag %d: %v\n", depth, c)
			f(c, append(desendants, n.Data))
		}
	}
	f(doc, desendants)

	for _, a := range cleanUp {
		a.parent.RemoveChild(a.child)
	}

	outputFile := getOutputFile(geturl)
	defer outputFile.Close()

	var buf bytes.Buffer
	bufW := bufio.NewWriter(&buf)
	html.Render(bufW, doc)
	bufW.Flush()

	bufR := bufio.NewReader(&buf)
	//io.Copy(outputFile, bufR)

	m := &minify.M{}
	htmlMin.Minify(m, outputFile, bufR,  map[string]string{"inline":"1"})
}

func handleCSS(cfg Config, geturl string,body io.ReadCloser) {
	outputFile := getOutputFile(geturl)

	m := &minify.M{}
	css.Minify(m, outputFile, body, map[string]string{"inline":"0"})
	body.Close()
	outputFile.Close()


	cssFile,_ := ioutil.ReadFile(outputFile.Name())
	cssStr := string(cssFile)
	outputFile.Close()

	cssStr = strings.Replace(cssStr,";",";\n",-1)
	cssStr = strings.Replace(cssStr,"}","}\n",-1)

	//fmt.Printf("CSS: %s", cssStr)
	s := scanner.New(cssStr)
	t := s.Next()
	for t.Type != scanner.TokenEOF {
		//fmt.Printf("Token: %v\n", t);
		if t.Type == scanner.TokenError {
			fmt.Printf("Error: %v\n", t);
			break
		}
		if t.Type == scanner.TokenURI {
			incUrl := strings.Replace(strings.Replace(t.Value, "url(", "", 1),")","", 1)
			incUrl = strings.Trim(incUrl, "\"'");

			incUrl,_ = getAbsoluteURL(cfg, geturl, incUrl)
			if len(incUrl) > 0 {
				fmt.Printf("URL: %s\n", incUrl);
				urls <- incUrl
			}

		}
		t = s.Next()
	}
}

func getAbsoluteURL(cfg Config, currentUrl, rawUrl string) (string,string) {

	rawUrl = strings.Trim(rawUrl, " ")

	// empty href=""
	if len(rawUrl) == 0 {
		return "", "";
	}
	// #id urls
	if rawUrl[0] == '#' {
		return "", rawUrl;
	}
	// href="javascript:..."
	if len(rawUrl) > 15 && rawUrl[0:11] == "javascript:" {
		return "", rawUrl;
	}
	// Ignore home links = starting point
	if len(rawUrl) == 1 && rawUrl[0] == '/' {
		return "", rawUrl;
	}
	// http(s)://otherdomain
	if (strings.Index(rawUrl, "http://") == 0 || strings.Index(rawUrl, "https://") == 0) && strings.Index(rawUrl, cfg.FullPath()) != 0 {
		return "", strings.Replace(rawUrl, "http://", "//", 1)
	}
	// http://mydomain.com...
	if strings.Index(rawUrl, cfg.FullPath()) == 0 {
		return rawUrl,strings.Replace(rawUrl, cfg.proto+"://"+cfg.domain, "", 1)
	}
	// //mydomain.com...
	if strings.Index(rawUrl, "//"+cfg.domain) == 0 {
		return "http:" + rawUrl, strings.Replace(rawUrl, "//"+cfg.domain, "", 1)
	}
	// //otherdomain...
	if rawUrl[0:2] == "//" && strings.Index(rawUrl, "//"+cfg.domain) != 0 {
		return "", rawUrl
	}
	if rawUrl[0:2] != "//" && rawUrl[0] == '/' {
		return fmt.Sprintf("%s://%s%s", cfg.proto, cfg.domain, rawUrl), rawUrl
	}

	u,_ := url.Parse(currentUrl)
	pathBits := strings.Split(u.Path, "/")
	pathDir := strings.Join(pathBits[0:len(pathBits)-1], "/")
	newUrl := fmt.Sprintf("%s://%s%s/%s", u.Scheme, u.Host, pathDir, rawUrl)

	return  newUrl, rawUrl
}

func getOutputFile(geturl string) *os.File {
	urlBits, err := url.Parse(geturl)
	ext := filepath.Ext(urlBits.Path)

	var outputFile *os.File
	var outputFilename string;

	pwd, err := os.Getwd()
	checkErr("Can't find working directory", err)

	fmt.Printf("Extension: %s\n", ext)

	if len(ext) > 0 {
		outputFilename = path.Join(pwd, "files", urlBits.Path)
	} else {
		outputFilename = path.Join(pwd, "files", urlBits.Path, "index.html")
	}
	os.MkdirAll(filepath.Dir(outputFilename), 0755)
	outputFile, err = os.Create(outputFilename)
	checkErr("creating file", err)

	return outputFile
}

func inArray(needle string, haystack []string) bool {
	for _, a := range haystack {
		if needle == a {
			return true;
		}
	}
	return false;
}

func getAttr(attrs []html.Attribute, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			return a.Val
		}
	}
	return "";
}
func setAttr(n *html.Node, attrs []html.Attribute, key, val string) {
	for i, a := range attrs {
		if a.Key == key {
			attrs[i].Val = val
		}
	}
	n.Attr = attrs
}
