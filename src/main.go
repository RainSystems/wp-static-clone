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
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"mime"
	"github.com/aws/aws-sdk-go/service/cloudfront"
	"math/rand"
	"strconv"
)

var wg sync.WaitGroup
var urls chan string
var invalidate chan string

type Config struct{
	domain,newDomain,proto,bucket,s3region string
	redisPool *pool.Pool
	extraUrls []string
	bucketList map[string]int64
	runId int64
}

func (c Config) FullPath() string {
	return fmt.Sprintf("%s://%s", c.proto, c.domain)
}


func main() {
	http.HandleFunc("/fetch", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Fetching: %s<br>\n", "dev-portable.pantheonsite.io");
		fetch()
		fmt.Fprintln(w, "Done");
	});
	http.HandleFunc("/clear-cache", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Clearing cache");
		redisPool, err := pool.New("tcp", "localhost:6379", 10)
		if err != nil {
			fmt.Fprintln(w, "Redis Error");
			return;
		}
		conn, _ := redisPool.Get()
		resp := conn.Cmd("FLUSHALL")
		if resp.Err != nil {
			fmt.Fprintln(w, "Redis Error");
			return;
		}
		fmt.Fprintln(w, "Done");
	});
	fmt.Println("Listening on 8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func fetch() {
	urls = make(chan string, 1000)
	invalidate = make(chan string, 100000)

	redisPool, err := pool.New("tcp", "localhost:6379", 10)
	checkErr("Failed to connect to Redis", err)
	cfg := Config{
		domain:"dev-portable.pantheonsite.io",
		newDomain:"portable.com.au",
		bucket:"portable.com.au",
		s3region:  "ap-southeast-2",
		proto:"http",
		redisPool: redisPool,
		runId: time.Now().Unix(),
		extraUrls: []string{
			"http://dev-portable.pantheonsite.io/wp-content/themes/bridge/css/elegant-icons/fonts/ElegantIcons.woff",
			"http://dev-portable.pantheonsite.io/wp-content/uploads/useanyfont/160629015139copernicus-book.woff",
			"http://dev-portable.pantheonsite.io/wp-content/themes/bridge/css/font-awesome/fonts/fontawesome-webfont.woff2?v=4.5.0",
			"http://dev-portable.pantheonsite.io/wp-content/themes/bridge/css/font-awesome/fonts/fontawesome-webfont.woff?v=4.5.0",
			"http://dev-portable.pantheonsite.io/wp-content/themes/bridge/css/font-awesome/fonts/fontawesome-webfont.ttf?v=4.5.0",
			"http://dev-portable.pantheonsite.io/wp-content/themes/bridge/css/elegant-icons/fonts/ElegantIcons.ttf",
			"http://dev-portable.pantheonsite.io/wp-content/themes/bridge/css/elegant-icons/fonts/ElegantIcons.ttf",
			"http://dev-portable.pantheonsite.io/wp-content/uploads/useanyfont/160629015413gt-walsheim-bold-ttf.woff",
			"http://dev-portable.pantheonsite.io/wp-includes/js/wp-emoji-release.min.js?ver=4.6.1",
			"http://dev-portable.pantheonsite.io/wp-content/uploads/2016/06/00c38cBigGreen.png?id=958",
			"http://dev-portable.pantheonsite.io/wp-content/uploads/2016/06/00c38c-slide.png",
			"http://dev-portable.pantheonsite.io/companies-weve-transformed/grace-papers",
		},
	}
	cfg.bucketList = make(map[string]int64, 100000)
	svc := s3.New(session.New(), &aws.Config{Region: aws.String(cfg.s3region)})
	err = svc.ListObjectsPages(&s3.ListObjectsInput{Bucket:&cfg.bucket},
		func(objs *s3.ListObjectsOutput, lastPage bool) bool {
			for _, obj := range objs.Contents {
				cfg.bucketList[*obj.Key] = *obj.Size
			}
			return true
		})
	checkErr("List Error", err)

	urls <- cfg.FullPath()
	for _, extra := range cfg.extraUrls {
		urls <- extra
	}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			for geturl := range urls {
				// a read from ch has occurred
				//fmt.Printf("Get: %s\n", geturl)
				getPage(cfg, geturl, false)
			}
		}()
	}
	wg.Wait()
}
const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func RandStringBytesRmndr(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Int63() % int64(len(letterBytes))]
	}
	return string(b)
}

func invalidateCloudfront() {
	invalidSet := make([]*string, 0)
	//close(invalidate)
	for inv := range invalidate {
		fmt.Printf("Inv: %s\n", inv)
		invalidSet = append(invalidSet, aws.String(inv))
	}

	cfSvc := cloudfront.New(session.New())

	total := int64(len(invalidSet))

	fmt.Printf("Invalidate: %v\n", invalidSet)

	_,err := cfSvc.CreateInvalidation(&cloudfront.CreateInvalidationInput{
		DistributionId: aws.String("E3U5ZY3M99CJ9M"), // Required
		InvalidationBatch: &cloudfront.InvalidationBatch{ // Required
			CallerReference: aws.String(RandStringBytesRmndr(15)), // Required
			Paths: &cloudfront.Paths{ // Required
				Quantity: aws.Int64(total), // Required
				Items: invalidSet,
			},
		},
	})
	checkErr("CloudFront Error", err)
}


func putS3(bucketName, key, filename string) {
	fmt.Printf("Upload: %s\n", filename)
	data,_ := ioutil.ReadFile(filename)
	ext := path.Ext(filename)
	client := s3.New(session.New(), &aws.Config{Region: aws.String("ap-southeast-2")})
	_,err := client.PutObject(&s3.PutObjectInput{
		Bucket:             aws.String(bucketName), // Required
		Key:                aws.String(key), // Required
		Body:               bytes.NewReader(data),
		ContentType:        aws.String(mime.TypeByExtension(ext)),
	})
	checkErr(fmt.Sprintf("Failed Put (%s)", filename), err)
	wg.Done()
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

func addUrlToQueue(cfg Config, newUrl string) {
	redis, _ := cfg.redisPool.Get()
	urlResp := redis.Cmd("GET", "added_"+newUrl)
	if urlResp.Err != nil {
		fmt.Println("Redis Error")
	} else {
		str, _ := urlResp.Str()
		if str == strconv.FormatInt(cfg.runId, 10) {
			//fmt.Println("In Queue")
			return
		}
	}
	urls <- newUrl
	redis.Cmd("SET", "added_"+newUrl, strconv.FormatInt(cfg.runId, 10))
}

func getPage(cfg Config, geturl string, noCache bool) {

	urlBits,_ := url.Parse(geturl)
	ext := path.Ext(urlBits.Path)
	if ext == ".html" || len(ext) == 0 {
		noCache = true;
	}

	if noCache != true {
		redis, _ := cfg.redisPool.Get()
		urlResp := redis.Cmd("GET", geturl)
		if urlResp.Err != nil {
			fmt.Println("Redis Error")
		} else {
			str, _ := urlResp.Str()
			if str == "1" {
//				fmt.Println("Cached")
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
			fmt.Printf("Failed %s: %s\n",resp.Status,geturl)
			return
		}


		if (resp.StatusCode == 301) {
			newUrl := resp.Header.Get("location");
			addUrlToQueue(cfg, newUrl)
			return
		}

		data := "1"
		redis,_ := cfg.redisPool.Get()
		if putResp := redis.Cmd("SET", geturl, data); putResp.Err != nil {
			fmt.Printf("Redis error: %v", putResp.Err)
		}

		ct := resp.Header.Get("Content-Type")

		if ct[0:8] == "text/css" {
			handleCSS(cfg, geturl, resp.Body)
			resp.Body.Close()
			return
		} else if ct[0:9] == "text/html" {
			handleHTML(cfg, geturl, resp.Body)
			resp.Body.Close()
			return
		} else {
			of := getOutputFile(geturl, "")

			io.Copy(of, resp.Body)

			resp.Body.Close()

			replaceString(cfg, of)

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
				//fmt.Println("Follow Links: " + aUrl)
				addUrlToQueue(cfg, aUrl)
			}
		}
		if n.Type == html.ElementNode && n.Data == "form" {
			_,updatedAttr := getAbsoluteURL(cfg, geturl, getAttr(n.Attr, "action"))
			setAttr(n, n.Attr, "action", updatedAttr)
		}
		if n.Type == html.ElementNode && n.Data == "meta" {
			//fmt.Printf("Meta: %s\n", getAttr(n.Attr, "name"));
			if inArray(getAttr(n.Attr, "name"), dropMeta) {
				cleanUp = append(cleanUp, parentChild{
					parent:n.Parent,
					child:n,
				})
			}
		}
		if n.Type == html.ElementNode && n.Data == "link" {
			//fmt.Printf("Link Rel: %s\n", getAttr(n.Attr, "rel"));
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
				if cssUrl != "" {
					updatedAttr = addExt(updatedAttr, ".css")
				}
				setAttr(n, n.Attr, "href", updatedAttr)
				if len(cssUrl) > 0 {
					//fmt.Println("Get CSS: " + cssUrl)
					addUrlToQueue(cfg, cssUrl)

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
				//fmt.Println("Get Img: " + imgUrl)
				addUrlToQueue(cfg, imgUrl)
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
					//fmt.Println("Get Img: " + imgUrl)
					addUrlToQueue(cfg, imgUrl)
				}
			}
			setAttr(n, n.Attr, "srcset", strings.Join(updatedSet, ","));
		}
		if n.Type == html.ElementNode && n.Data == "script" {
			scriptUrl,updatedAttr := getAbsoluteURL(cfg, geturl, getAttr(n.Attr, "src"))
			setAttr(n, n.Attr, "src", updatedAttr);
			if len(scriptUrl) > 0 {
				//fmt.Println("Get Script: " + scriptUrl)
				addUrlToQueue(cfg, scriptUrl)
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

	outputFile := getOutputFile(geturl, "")
	defer outputFile.Close()

	var buf bytes.Buffer
	bufW := bufio.NewWriter(&buf)
	html.Render(bufW, doc)
	bufW.Flush()

	bufR := bufio.NewReader(&buf)
	//io.Copy(outputFile, bufR)

	m := &minify.M{}
	htmlMin.Minify(m, outputFile, bufR,  map[string]string{"inline":"1"})

	replaceString(cfg, outputFile)

}

func addExt(fn, reqExt string) string  {
	ext := filepath.Ext(fn)
	if ext != reqExt {
		fn = fn+reqExt
	}
	return fn
}

func handleCSS(cfg Config, geturl string,body io.ReadCloser) {
	outputFile := getOutputFile(geturl, ".css")

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
				addUrlToQueue(cfg, incUrl)
			}

		}
		t = s.Next()
	}

	replaceString(cfg, outputFile)

}

func replaceString(cfg Config, f *os.File) {
	file,_ := ioutil.ReadFile(f.Name())
	content := string(file)
	content = strings.Replace(content, cfg.FullPath()+"/", "/", -1)
	content = strings.Replace(content, cfg.proto+":\\/\\/"+cfg.domain+"\\/", "\\/", -1)

	stat,_ := os.Stat(f.Name())

	ioutil.WriteFile(f.Name(), []byte(content), stat.Mode())

	pwd,_ := os.Getwd()
	key := f.Name()[len(path.Join(pwd,"files"))+1:]

	if val, ok := cfg.bucketList[key]; ok {
		if (stat.Size() == val || stat.Size()+1 == val) {
			// Same Size
		} else {
			wg.Add(1)
			invalidate <- "/"+key
			go putS3(cfg.bucket, key, f.Name());
		}
	} else {
		wg.Add(1)
		go putS3(cfg.bucket, key, f.Name());
	}

}

func getAbsoluteURL(cfg Config, currentUrl, rawUrl string) (string,string) {
	newUrl, replace := _getAbsoluteURL(cfg, currentUrl, rawUrl)

	if newUrl != "" {
		u,_ := url.Parse(replace)
		return newUrl, u.Path
	}

	return newUrl, replace;
}
func _getAbsoluteURL(cfg Config, currentUrl, rawUrl string) (string,string) {

	rawUrl = strings.Trim(rawUrl, " ")

	// empty href=""
	if len(rawUrl) == 0 {
		return "", "";
	}
	// #mailto:
	if len(rawUrl) >6 && rawUrl[0:7] == "mailto:" {
		return "", rawUrl;
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
	// /base/path
	if rawUrl[0:2] != "//" && rawUrl[0] == '/' {
		return fmt.Sprintf("%s://%s%s", cfg.proto, cfg.domain, rawUrl), rawUrl
	}

	// relative/path
	u,_ := url.Parse(currentUrl)
	pathBits := strings.Split(u.Path, "/")
	pathDir := strings.Join(pathBits[0:len(pathBits)-1], "/")
	newUrl := fmt.Sprintf("%s://%s%s/%s", u.Scheme, u.Host, pathDir, rawUrl)

	return  newUrl, rawUrl
}

func getOutputFile(geturl string, reqExt string) *os.File {
	urlBits, err := url.Parse(geturl)

	if len(reqExt) > 0 {
		urlBits.Path = addExt(urlBits.Path, reqExt)
	}

	ext := filepath.Ext(urlBits.Path)


	var outputFile *os.File
	var outputFilename string;

	pwd, err := os.Getwd()
	checkErr("Can't find working directory", err)

	//fmt.Printf("Extension: %s\n", ext)

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
