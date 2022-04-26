// Package main 漫画猫漫画
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"golang.org/x/net/html"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FloatTech/zbputils/binary"
	"github.com/FloatTech/zbputils/file"
	"github.com/FloatTech/zbputils/web"
	lzstring "github.com/Lazarus/lz-string-go"
	"github.com/antchfx/htmlquery"
	"github.com/wdvxdr1123/ZeroBot/message"
)

var (
	dbpath        = "data/maofly/"
	imgPre        = "https://mao.mhtupian.com/uploads/"
	searchURL     = "https://www.maofly.com/search.html?q="
	re            = regexp.MustCompile(`let img_data = "(.*?)"`)
	chanImageUrls chan string
	taskNum       int64
	waitGroup     sync.WaitGroup
	files         = make([]string, 0, 32)
	ua            = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/100.0.4896.127 Safari/537.36"
	unDownloadURL string
)

func main() {
	_ = os.MkdirAll(dbpath, 0755)
	taskNum = 0
	unDownloadURL = ""
	files = files[:0]
	chanImageUrls = make(chan string, 100000)

	var keyword string
	fmt.Println("请输入漫画名：")
	_, _ = fmt.Scanln(&keyword)
	// keyword = "露出导演"
	text, comic := search(keyword)
	if len(comic) == 0 {
		fmt.Println(message.Text("没有找到与", keyword, "有关的漫画"))
		return
	}
	text += "\n"
	for i := 0; i < len(comic); i++ {
		text += fmt.Sprintf("%d. [%s]%s\n", i, comic[i].title, comic[i].href)
	}
	text += "请输入下载的漫画序号:"
	fmt.Println(text)
	var num int
	_, _ = fmt.Scanln(&num)
	// num = 0
	indexURL := comic[num].href
	title, c, err := getChapter(indexURL)
	if err != nil {
		fmt.Println(message.Text("ERROR:", err))
		return
	}
	if len(c) == 0 {
		fmt.Println(message.Text(title, "已下架"))
		return
	}
	zipName := dbpath + title + ".zip"
	if file.IsExist(zipName) {
		err := unzip(zipName, ".")
		if err != nil {
			_ = os.RemoveAll(title)
			fmt.Println(message.Text("ERROR:", err))
			return
		}
	} else {
		_ = os.MkdirAll(title, 0755)
	}

	waitGroup.Add(len(c))
	for i := 0; i < len(c); i++ {
		go getImgs(title, i, c[i])
	}

	waitGroup.Add(1)
	go func(total int) {
		for {
			if taskNum == int64(total) {
				close(chanImageUrls)
				break
			}
		}
		waitGroup.Done()
	}(len(c))

	routineCount := 5
	waitGroup.Add(routineCount)
	for i := 0; i < routineCount; i++ {
		go downloadFile()
	}
	waitGroup.Wait()

	_ = os.WriteFile(title+"/unDownloadURL.txt", binary.StringToBytes(unDownloadURL), 0666)
	files = append(files, title+"/unDownloadURL.txt")

	if err := zipFiles(zipName, files); err != nil {
		_ = os.RemoveAll(title)
		fmt.Println(message.Text("ERROR:", err))
		return
	}
	fmt.Println("压缩的文件名:", zipName)
	_ = os.RemoveAll(title)

}

type chapter struct {
	dataSort int
	href     string
	title    string
}

type chapterSlice []chapter

func (c chapterSlice) Len() int {
	return len(c)
}
func (c chapterSlice) Swap(i, j int) {
	c[i], c[j] = c[j], c[i]
}
func (c chapterSlice) Less(i, j int) bool {
	return c[i].dataSort > c[j].dataSort
}

type a struct {
	href  string
	title string
}

func search(key string) (text string, al []a) {
	requestURL := searchURL + url.QueryEscape(key)
	data, err := web.RequestDataWith(web.NewDefaultClient(), requestURL, "GET", "", ua)
	if err != nil {
		fmt.Println("[maofly]", err)
		return
	}
	doc, err := htmlquery.Parse(bytes.NewReader(data))
	if err != nil {
		fmt.Println("[maofly]", err)
		return
	}
	text = htmlquery.FindOne(doc, "//div[@class=\"text-muted\"]/text()").Data
	list, err := htmlquery.QueryAll(doc, "//h2[@class=\"mt-0 mb-1 one-line\"]/a")
	if err != nil {
		fmt.Println("[maofly]", err)
		return
	}
	al = make([]a, len(list))
	for i := 0; i < len(list); i++ {
		al[i].href = list[i].Attr[0].Val
		al[i].title = list[i].Attr[1].Val
	}
	return
}

func getChapter(indexURL string) (title string, c chapterSlice, err error) {
	data, err := web.RequestDataWith(web.NewDefaultClient(), indexURL, "GET", "", ua)
	if err != nil {
		return
	}
	doc, err := htmlquery.Parse(bytes.NewReader(data))
	if err != nil {
		return
	}
	title = htmlquery.FindOne(doc, "//meta[@property=\"og:novel:book_name\"]").Attr[1].Val
	list, err := htmlquery.QueryAll(doc, "//*[@id=\"comic-book-list\"]/div/ol/li")
	if err != nil {
		return
	}
	c = make(chapterSlice, len(list))
	for i := 0; i < len(list); i++ {
		c[i].dataSort, _ = strconv.Atoi(list[i].Attr[1].Val)
		var node *html.Node
		node, err = htmlquery.Query(list[i], "//a")
		if err != nil {
			return
		}
		c[i].href = node.Attr[1].Val
		c[i].title = node.Attr[2].Val
	}
	sort.Sort(c)
	return
}

func getImgs(title string, index int, c chapter) {
	var data []byte
	data, err := web.RequestDataWith(web.NewDefaultClient(), c.href, "GET", "", ua)
	for i := 1; err != nil && i <= 10; i++ {
		fmt.Println("[maofly]", err, ",", i, "s后重试")
		time.Sleep(time.Duration(i) * time.Second)
		data, err = web.RequestDataWith(web.NewDefaultClient(), c.href, "GET", "", ua)
		if i == 10 {
			fmt.Println("[maofly]章节不完整,请重新下载")
			os.Exit(1)
		}
	}
	s := re.FindStringSubmatch(binary.BytesToString(data))[1]
	d, _ := lzstring.Decompress(s, "")
	imgs := strings.Split(d, ",")
	for i := 0; i < len(imgs); i++ {
		dir := fmt.Sprintf("%s/%04d %s", title, index, c.title)
		_ = os.MkdirAll(dir, 0755)
		fileURL := imgPre + path.Dir(imgs[i]) + "/" + strings.ReplaceAll(url.QueryEscape(path.Base(imgs[i])), "+", "%20")
		filePath := fmt.Sprintf("%s/%s-%d-%03d%s", dir, title, index, i+1, path.Ext(imgs[i]))
		dkey := filePath + "|" + fileURL
		chanImageUrls <- dkey
	}
	atomic.AddInt64(&taskNum, 1)
	waitGroup.Done()
}

func downloadFile() {
	for dkey := range chanImageUrls {
		filePath := strings.Split(dkey, "|")[0]
		fileURL := strings.Split(dkey, "|")[1]
		if file.IsExist(filePath) {
			files = append(files, filePath)
			continue
		}
		data, err := web.RequestDataWith(web.NewDefaultClient(), fileURL, "GET", "", ua)
		if err != nil {
			unDownloadURL += fileURL + "\n"
			data = binary.StringToBytes(fileURL)
			filePath = strings.ReplaceAll(filePath, path.Ext(filePath), ".txt")
			fmt.Println("[maofly]下载", fileURL, "时出现:", err)
		}
		_ = os.WriteFile(filePath, data, 0666)
		files = append(files, filePath)
	}
	waitGroup.Done()
}

func zipFiles(filename string, files []string) error {
	newZipFile, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer newZipFile.Close()

	zipWriter := zip.NewWriter(newZipFile)
	defer zipWriter.Close()

	for _, file := range files {
		if err = addFileToZip(zipWriter, file); err != nil {
			return err
		}
	}
	return nil
}

func addFileToZip(zipWriter *zip.Writer, filename string) error {
	fileToZip, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer fileToZip.Close()

	info, err := fileToZip.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}

	header.Name = filename

	header.Method = zip.Deflate

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, fileToZip)
	return err
}

func unzip(zipFile string, destDir string) error {
	zipReader, err := zip.OpenReader(zipFile)
	if err != nil {
		return err
	}
	defer zipReader.Close()

	for _, f := range zipReader.File {
		fpath := filepath.Join(destDir, f.Name)
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(fpath, os.ModePerm)
		} else {
			if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
				return err
			}

			inFile, err := f.Open()
			if err != nil {
				return err
			}
			defer inFile.Close()

			outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return err
			}
			defer outFile.Close()

			_, err = io.Copy(outFile, inFile)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
