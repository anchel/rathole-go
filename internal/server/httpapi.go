package server

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/anchel/rathole-go/internal/common"
	"github.com/anchel/rathole-go/internal/config"
)

//go:embed fe
var frontend embed.FS

func do_httpapi(s *Server, conn *net.TCPConn, req *http.Request) {
	defer conn.Close()
	defer req.Body.Close()

	uri := req.URL.Path

	// 如果请求的 URI 以 /fe 开头，返回前端文件
	if strings.HasPrefix(uri, "/fe/") {
		err := serveFrontend(req, conn)
		if err != nil {
			fmt.Println("serveFrontend fail", err)
		}
		return
	}

	switch uri {
	case "/favicon.ico":
		// fmt.Println("favicon.ico")
		// 解码 Base64 字符串
		decodedBytes, err := base64.StdEncoding.DecodeString(faviconIcon)
		if err != nil {
			fmt.Println("Error decoding base64:", err)
		} else {
			writeBytes(decodedBytes, conn, false, "image/x-icon")
		}
	case "/":
		serveHome(s, conn, req)

	case "/api/service/add":
		fmt.Println("add service")
		// 检查请求是否包含 Authorization 头
		if !checkAuthorization(s, conn, req) {
			return
		}
		writeBytes([]byte("add service"), conn, false, "text/plain")

	case "/api/service/delete":
		err := serviceDelete(s, conn, req)
		if err != nil {
			fmt.Println("serviceDelete fail", err)
		}

	default:
		fmt.Println("unknown uri", uri)
		err := notFound(conn)
		if err != nil {
			fmt.Println("notFound fail", err)
		}
		return
	}
}

func serviceDelete(s *Server, conn *net.TCPConn, req *http.Request) error {
	fmt.Println("delete service")

	// 检查请求是否包含 Authorization 头
	if !checkAuthorization(s, conn, req) {
		return nil
	}

	reqBody, err := io.ReadAll(req.Body)
	if err != nil {
		fmt.Println("read request body fail", err)
		return err
	}

	st := struct {
		Service string `json:"service"`
	}{}

	err = json.Unmarshal(reqBody, &st)
	if err != nil {
		fmt.Println("json.Unmarshal fail", err)
		return err
	}

	fmt.Println("request body:", string(reqBody))

	conf, err := config.GetConfig()
	if err != nil {
		fmt.Println("config.GetConfig fail", err)
		return err
	}
	if conf == nil {
		fmt.Println("config.GetConfig fail, conf is nil", err)
		return err
	}

	_, ok := conf.Server.Services[st.Service]
	if !ok {
		fmt.Println("service not exists", st.Service)
		err = writeBytes([]byte(`{"code": 1, "message": "service not exists"}`), conn, true, "application/json")
		if err != nil {
			fmt.Println("writeBytes fail", err)
		}
		return errors.New("service not exists")
	}

	delete(conf.Server.Services, st.Service)

	err = config.WriteFile(conf)
	if err != nil {
		fmt.Println("config.WriteFile fail", err)
		return err
	}

	return writeBytes([]byte(`{"code": 0, "message": "success"}`), conn, true, "application/json")
}

func serveHome(s *Server, conn *net.TCPConn, req *http.Request) error {
	fmt.Println("home")
	// 检查请求是否包含 Authorization 头
	if !checkAuthorization(s, conn, req) {
		return nil
	}

	bsTpl, err := frontend.ReadFile("fe/template/index.html")
	if err != nil {
		fmt.Println("read index.html file fail", err)
		return err
	}

	renderData := struct {
		BindAddr string
		Services []struct {
			Name           string
			Type           config.ServiceType
			Token          string
			BindAddr       string
			ControlChannel string
			DataChannel    int32
		}
	}{}

	renderData.BindAddr = s.config.BindAddr
	for k, v := range s.config.Services {
		svc := struct {
			Name           string
			Type           config.ServiceType
			Token          string
			BindAddr       string
			ControlChannel string
			DataChannel    int32
		}{
			Name:     k,
			Type:     v.Type,
			Token:    v.Token,
			BindAddr: v.BindAddr,
		}

		serviceDigest := common.CalSha256(k)
		cc := s.controlChannelManager.Get(serviceDigest, "")
		if cc != nil {
			ncc, ok := cc.(*ControlChannel)
			if ok {
				svc.ControlChannel = ncc.ClientAddr()
				svc.DataChannel = ncc.NumDataChannel()
			}
		}

		renderData.Services = append(renderData.Services, svc)
	}

	tmpl, err := template.New("index").Parse(string(bsTpl))

	if err != nil {
		fmt.Println("template.New fail", err)
		return err
	}

	buf := bytes.NewBuffer(nil)

	err = tmpl.Execute(buf, renderData)
	if err != nil {
		fmt.Println("tmpl.Execute fail", err)
		return err
	}

	err = writeBytes(buf.Bytes(), conn, false, "text/html")
	if err != nil {
		fmt.Println("writeBytes fail", err)
	}
	return err
}

func checkAuthorization(s *Server, conn *net.TCPConn, req *http.Request) bool {
	// 如果未启用认证，直接返回 true
	if !s.authEnabled() {
		return true
	}

	// 检查请求是否包含 Authorization 头
	authHeader := req.Header.Get("Authorization")

	fmt.Println("Authorization:", authHeader)

	// 如果没有提供 Authorization 头，返回 401 未授权错误，并设置 WWW-Authenticate 头
	if authHeader == "" {
		fmt.Println("Authorization is empty")
		err := auth(conn)
		if err != nil {
			fmt.Println("auth fail", err)
		}
		return false
	}
	// 检查 Authorization 头的用户名和密码
	username, password, ok := req.BasicAuth()
	if !ok {
		fmt.Println("parse Authorization fail")
		err := auth(conn)
		if err != nil {
			fmt.Println("auth fail", err)
		}
		return false
	}
	fmt.Println("username:", username, "password:", password)

	// 如果用户名和密码不匹配，返回 401 未授权错误，并设置 WWW-Authenticate 头
	if username != s.config.AuthUsername || password != s.config.AuthPassword {
		fmt.Println("username or password not match")
		err := auth(conn)
		if err != nil {
			fmt.Println("auth fail", err)
		}
		return false
	}

	return true
}

func auth(conn *net.TCPConn) error {
	resp := &http.Response{
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Status:        "401 Unauthorized",
		StatusCode:    http.StatusUnauthorized,
		Header:        make(map[string][]string),
		ContentLength: 0,
	}
	resp.Header.Set("WWW-Authenticate", `Basic realm="Restricted"`)
	err := common.ResponseWriteWithBuffered(resp, conn)
	if err != nil {
		fmt.Println("response fail", err)
		return err
	}
	return nil
}

func notFound(conn *net.TCPConn) error {
	bs, err := frontend.ReadFile("fe/template/404.html")
	if err != nil {
		fmt.Println("read file fail", err)
		return err
	}

	buf := bytes.NewBuffer(bs)
	rc := &ByteBufferReadCloser{buf}

	resp := &http.Response{
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Status:        "404 Not Found",
		StatusCode:    http.StatusNotFound,
		Header:        make(map[string][]string),
		Body:          rc,
		ContentLength: int64(buf.Len()),
	}
	err = common.ResponseWriteWithBuffered(resp, conn)
	if err != nil {
		fmt.Println("response fail", err)
		return err
	}
	return nil
}

func serveFrontend(req *http.Request, conn *net.TCPConn) error {
	uri := strings.TrimPrefix(req.URL.Path, "/")

	bs, err := frontend.ReadFile(uri)
	if err != nil {
		fmt.Println("read file fail", err)
		err = notFound(conn)
		if err != nil {
			fmt.Println("notFound fail", err)
		}
		return err
	}
	contentType := ""
	if strings.HasSuffix(uri, ".js") {
		contentType = "text/javascript; charset=utf-8"
	} else if strings.HasSuffix(uri, ".css") {
		contentType = "text/css; charset=utf-8"
	} else if strings.HasSuffix(uri, ".png") {
		contentType = "image/png"
	} else if strings.HasSuffix(uri, ".ico") {
		contentType = "image/x-icon"
	}
	err = writeBytes(bs, conn, false, contentType)
	if err != nil {
		fmt.Println("writeBytes fail", err)
	}
	return err
}

func writeBytes(bs []byte, conn *net.TCPConn, isJson bool, contentType string) error {
	buf := bytes.NewBuffer(bs)
	rc := &ByteBufferReadCloser{buf}

	resp := &http.Response{
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Header:        make(map[string][]string),
		Body:          rc,
		ContentLength: int64(buf.Len()),
	}
	if isJson {
		resp.Header.Set("Content-Type", "application/json")
	}
	if contentType != "" {
		resp.Header.Set("Content-Type", contentType)
	}

	err := common.ResponseWriteWithBuffered(resp, conn)
	if err != nil {
		fmt.Println("response fail", err)
	}
	return err
}

// 自定义类型 ByteBufferReadCloser，它封装了 bytes.Buffer
type ByteBufferReadCloser struct {
	*bytes.Buffer
}

// 实现 io.ReadCloser 接口
func (b *ByteBufferReadCloser) Close() error {
	// 对于 bytes.Buffer，Close 方法可以是一个空方法
	// 因为 bytes.Buffer 并不需要显式地关闭
	return nil
}

const faviconIcon = "/9j/4AAQSkZJRgABAQAAAQABAAD//gA8Q1JFQVRPUjogZ2QtanBlZyB2MS4wICh1c2luZyBJSkcgSlBFRyB2NjIpLCBxdWFsaXR5ID0gMTAwCv/bAEMAAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAf/bAEMBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAf/AABEIABAAEAMBIgACEQEDEQH/xAAfAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgv/xAC1EAACAQMDAgQDBQUEBAAAAX0BAgMABBEFEiExQQYTUWEHInEUMoGRoQgjQrHBFVLR8CQzYnKCCQoWFxgZGiUmJygpKjQ1Njc4OTpDREVGR0hJSlNUVVZXWFlaY2RlZmdoaWpzdHV2d3h5eoOEhYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPExcbHyMnK0tPU1dbX2Nna4eLj5OXm5+jp6vHy8/T19vf4+fr/xAAfAQADAQEBAQEBAQEBAAAAAAAAAQIDBAUGBwgJCgv/xAC1EQACAQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEKFiQ04SXxFxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T19vf4+fr/2gAMAwEAAhEDEQA/AP7JP29v2v8Awd+wz+y98R/2h/GMN3fP4etbXQ/CWh2VslzNr3j3xNN/ZfhLS5TLPbQWumtqcq3ut389wn2TRLLUbi2gv76O0028yv8Agnj+2L4U/bp/ZS+HHx88NJqNreahBceFfHmm6raQWt5pHxF8LCCw8VWimzL2F3p93dNDrGj31kY4rrR9UsHms9MvDdaVZed/tcQ63+0f8N/it+zF8Q/2MPid8QPh745tvEfhi48Y6drfwvn0SytbTUJ7Tw3470S38QeLND1v/hJdLmh0vxvo9jb6Y1vBCIbNdZvNXh1LQoqn7F2leJP2bfhx8Ff2Y/CX7HXxB8AfDbwt4aTStb8eXXiz4d3sI8US3ka3niTXrGy1w6zrV14lYat4j8R6w9vZTafM+naZp+j3CXUVjo/V7fKv7J+rf2Ziv7a+vOv/AGz/AGovqay72Eaf9nf2N9T1rvEXxH9ofX2+T9wsOlzTk+XEfWPafWIfVfZKH1X6s/ae252/b/WvafByWh7D2Nr3nz9F/9k="
