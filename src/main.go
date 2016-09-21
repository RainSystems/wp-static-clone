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
)

var wg sync.WaitGroup


type Config struct{
	domain string
	proto string
	redisPool *pool.Pool
}

func (c Config) FullPath() string {
	return fmt.Sprintf("%s://%s", c.proto, c.domain)
}

func main() {

	redisPool, err := pool.New("tcp", "localhost:6379", 10)
	cfg := Config{
		domain:"dev-portable.pantheonsite.io",
		proto:"http",
		redisPool: redisPool,
	}

	//redis, _ := redisPool.Get()
	//redis.Cmd("FLUSHALL");
	//return

	if err != nil {
		// handle error
	}

	//wg.Add(2)
	//go getPage(cfg, "http://dev-portable.pantheonsite.io/wp-content/uploads/useanyfont/160629014335gt-walsheim.woff", true)
	//go getPage(cfg, "http://dev-portable.pantheonsite.io/wp-content/uploads/useanyfont/160629014258gt-walsheim.woff", true)
	//wg.Wait()
	//os.Exit(0);

	for i := 200; i < 500; i++ {
		wg.Add(1)
		url := fmt.Sprintf(cfg.FullPath()+"/?p=%d", i);
		go getPage(cfg, url, false)
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
				wg.Done()
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
		wg.Done()
		return
	} else {
		if (resp.StatusCode >= 400) {
			println(resp.Status)
			wg.Done()
			return
		}


		if (resp.StatusCode == 301) {
			newUrl := resp.Header.Get("location");
			wg.Add(1);
			getPage(cfg, newUrl, false);
			wg.Done()
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
			wg.Done()
			return
		} else if ct == "text/html; charset=UTF-8" {
			handleHTML(cfg, geturl, resp.Body)
			resp.Body.Close()
			wg.Done()
			return
		} else {
			of := getOutputFile(geturl)

			io.Copy(of, resp.Body)

			resp.Body.Close()
			wg.Done()
			return
		}
		resp.Body.Close()
	}
	wg.Done()
}

func handleHTML(cfg Config, geturl string, body io.ReadCloser) {
	doc, err := html.Parse(body)
	if err != nil {
		log.Printf("Parse Err: %v\n", err)
		wg.Done()
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
				wg.Add(1)
				go getPage(cfg, aUrl, false)
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
					wg.Add(1)
					go getPage(cfg, cssUrl, false)
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
				wg.Add(1)
				go getPage(cfg, imgUrl, false)
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
					wg.Add(1)
					go getPage(cfg, imgUrl, false)
				}
			}

			setAttr(n, n.Attr, "srcset", strings.Join(updatedSet, ","));


		}
		if n.Type == html.ElementNode && n.Data == "script" {
			scriptUrl,updatedAttr := getAbsoluteURL(cfg, geturl, getAttr(n.Attr, "src"))
			setAttr(n, n.Attr, "src", updatedAttr);
			if len(scriptUrl) > 0 {
				fmt.Println("Get Srcript: " + scriptUrl)
				wg.Add(1)
				go getPage(cfg, scriptUrl, false)
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
				wg.Add(1)
				getPage(cfg, incUrl, false);
			}

		}
		t = s.Next()
	}
	fmt.Printf("Line: %d Col: %d",t.Line,t.Column)
}

func getAbsoluteURL(cfg Config, currentUrl, rawUrl string) (string,string) {

	rawUrl = strings.Trim(rawUrl, " ")

	if len(rawUrl) == 0 {
		return "", "";
	}
	if rawUrl[0] == '#' {
		return "", rawUrl;
	}
	if len(rawUrl) > 15 && rawUrl[0:15] == "javascript:void" {
		return "", rawUrl;
	}
	if len(rawUrl) == 1 && rawUrl[0] == '/' {
		return "", rawUrl;
	}
	if (strings.Index(rawUrl, "http://") == 0 || strings.Index(rawUrl, "https://") == 0) && strings.Index(rawUrl, cfg.FullPath()) != 0 {
		return "", strings.Replace(rawUrl, "http://", "//", 1)
	}
	if strings.Index(rawUrl, cfg.FullPath()) == 0 {
		return rawUrl,strings.Replace(rawUrl, cfg.proto+"://"+cfg.domain, "", 1)
	}
	if strings.Index(rawUrl, "//"+cfg.domain) == 0 {
		return "http:" + rawUrl, strings.Replace(rawUrl, "//"+cfg.domain, "", 1)
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
	ext := filepath.Ext(geturl)

	var outputFile *os.File
	var outputFilename string;

	pwd, err := os.Getwd()
	checkErr("Can't find working directory", err)

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
