package main

import (
	"net/http"
	"bytes"
	"strings"
	"fmt"
	"sync"
)

func main() {
	var wg sync.WaitGroup
	for i := 1; i < 100; i++ {
		wg.Add(1)
		go getPage(i)
	}
	wg.Wait()
}

func getPage(p int) {

	url := fmt.Sprintf("http://dev-portable.pantheonsite.io/?p=%d", p);
	client := &http.Client{
		CheckRedirect:func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		defer resp.Body.Close()
		println(resp.Status)
	} else {
		defer resp.Body.Close()
		if(resp.StatusCode >= 400) {
			return
		}
		println(resp.Status)
		println(resp.Header.Get("location"))
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		s := buf.String()
		s = strings.Replace(s, "dev-portable.pantheonsite.io", "portable.com.au", -1)
		s = strings.Replace(s, "http:", "https:", -1)

		//fmt.Printf("%s %s %s\nHost: %s\n", resp.Request.Method, resp.Request.RequestURI, resp.Request.Proto, resp.Request.Host)
	}
}
