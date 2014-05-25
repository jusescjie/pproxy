package serve

import (
	"encoding/base64"
	"fmt"
	"github.com/elazarl/goproxy"
	"github.com/elazarl/goproxy/ext/auth"
	"github.com/googollee/go-socket.io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"reflect"
	"strconv"
//	"strings"
	"time"
	"io/ioutil"
	 "math/rand"
	 "github.com/robertkrimen/otto"
	 "net/url"
	 "sync"
)

var js *otto.Otto
var jsFn otto.Value

type ProxyServe struct {
	Port      int
	Goproxy   *goproxy.ProxyHttpServer
	AdminName string
	AdminPsw  string
	mydb      *TieDb
	ws        *socketio.SocketIOServer
	wsClients map[string]*wsClient
	startTime time.Time

	MaxResSaveLength int64
	RewriteJs string
	mu sync.RWMutex
}
type wsClient struct {
	ns   *socketio.NameSpace
	user string
	filter_client_ip string
	filter_hide []string
	filter_url []string
}

type kvType map[string]interface{}

func (ser *ProxyServe) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	host, port, _ := net.SplitHostPort(req.Host)
	port_int, _ := strconv.Atoi(port)
	isLocalReq := port_int == ser.Port
	if isLocalReq {
		isLocalReq = IsLocalIp(host)
	}
	if isLocalReq {
		ser.handleLocalReq(w, req)
	} else {
		ser.Goproxy.ServeHTTP(w, req)
	}
}

func (ser *ProxyServe) Start() {
	ser.Goproxy = goproxy.NewProxyHttpServer()
	ser.Goproxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		authInfo := getAuthorInfo(req)
		uname:= "guest"
		if authInfo != nil {
			uname = authInfo.Name
		}
		for k, _ := range req.Header {
			if len(k) > 5 && k[:6] == "Proxy-" {
				req.Header.Del(k)
			}
		}
		if ser.AdminName != "" && (authInfo == nil || (authInfo != nil && !authInfo.isEqual(ser.AdminName, ser.AdminPsw))) {
			return nil, auth.BasicUnauthorized(req, "auth need")
		}
		logdata := kvType{}
		logdata["host"] = req.Host
		logdata["header"] = map[string][]string(req.Header)
		logdata["url"] = req.URL.String()
		logdata["path"] = req.URL.Path
		logdata["cookies"] = req.Cookies()
		logdata["now"] = time.Now().Unix()
		logdata["session_id"] = ctx.Session
		logdata["user"] = uname
		logdata["client_ip"] = req.RemoteAddr
		logdata["form_get"]=req.URL.Query()
		
		if(req.Header.Get("Content-Type")=="application/x-www-form-urlencoded"){
			buf:=forgetRead(&req.Body)
			post_vs,post_e:=url.ParseQuery(buf.String())
			if(post_e!=nil){
				log.Println("parse post err",post_e)
			}
			logdata["form_post"]=post_vs;
		}
		
		req_dump, err_dump := httputil.DumpRequest(req, true)
		
		if err_dump != nil {
			log.Println("dump request failed")
			req_dump = []byte("dump failed")
		}
		logdata["dump"] = base64.StdEncoding.EncodeToString(req_dump)
		req_uid := NextUid()+uint64(ctx.Session)
		
		ctx.UserData = req_uid
		
		ser.changeRequest(req)
		
		rewrite:=make(map[string]string)
		url_new:=req.URL.String()
		
		if(url_new!=logdata["url"]){
		   rewrite["url"]=url_new
		}
		
		logdata["rewrite"]=rewrite
		
		err := ser.mydb.RequestTable.InsertRecovery(req_uid, logdata)
		log.Println("save_req", ctx.Session, req.URL.String(), "req_docid=", req_uid, err,rewrite)
	
		if err != nil {
			log.Println(err)
			return req,nil
		}
		
		ser.Broadcast_Req(ctx.Session, req, req_uid)
		
		return req, nil
	})

	ser.Goproxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if resp == nil || resp.Request == nil {
			return resp
		}
		ser.logResponse(resp, ctx)
		return resp
	})

	addr := fmt.Sprintf("%s:%d", "", ser.Port)
	log.Println("proxy listen at ", addr)
	ser.initWs()
	err := http.ListenAndServe(addr, ser)
	log.Println(err)
}
func (ser *ProxyServe) changeRequest(req *http.Request) {
   if(js!=nil){
      urlObj, _ := js.Object(`ul={}`)
      urlObj.Set("url",req.URL.String())
      urlObj.Set("schema",req.URL.Scheme)
      urlObj.Set("host",req.URL.Host)
      urlObj.Set("path",req.URL.Path)
      urlObj.Set("rawquery",req.URL.RawQuery)
      urlObj.Set("fragment",req.URL.Fragment)
      urlObj.Set("opaque",req.URL.Opaque)
      username:=""
      psw:=""
      if(req.URL.User!=nil){
	      username=req.URL.User.Username()
	      psw,_=req.URL.User.Password()
      }
      urlObj.Set("username",username)
      urlObj.Set("password",psw)
      
      js_ret,err_js:=jsFn.Call(jsFn,urlObj)
      
      if(err_js==nil ){
	      if(js_ret.IsObject()){
          	obj,export_err:=js_ret.Export()
          	if(export_err==nil){
             	url_obj:=obj.(map[string]interface{})
             	url_new:=fmt.Sprintf("%s",url_obj["schema"])+"://";
             	username:=fmt.Sprintf("%s",url_obj["username"])
             	if(username!=""){
                	url_new+=fmt.Sprintf("%s:%s@",username,url_obj["password"])
             	}
             	url_new+=fmt.Sprintf("%s%s",url_obj["host"],url_obj["path"])
             	
             	if(url_new==req.URL.String()){
             	   return
             	}
             	
			    var url_err error
		        req.URL,url_err=req.URL.Parse(url_new)
		        if(url_err!=nil){
		           log.Println("js filter err:",js_ret,url_err)
			    }
          	}else{
          	   log.Println("js filter result wrong",js_ret.String())
          	}
	        }
      }else{
          log.Println("js filter err:",err_js,js_ret)
        }
      
   }
}

/**
*log response if the req has log
 */
func (ser *ProxyServe) logResponse(res *http.Response, ctx *goproxy.ProxyCtx) {
	if reflect.TypeOf(ctx.UserData).Kind() != reflect.Uint64 {
		log.Println("err,userdata not reqid,log res skip")
		return
	}
	req_uid := ctx.UserData.(uint64)
	data := kvType{}
	data["session_id"] = ctx.Session
	data["now"] = time.Now().Unix()
	data["header"] = map[string][]string(res.Header)
	data["status"] = res.StatusCode
	data["content_length"] = res.ContentLength

	res_dump, dump_err := httputil.DumpResponse(res, false)
	if dump_err != nil {
		log.Println("dump res err", dump_err)
		res_dump = []byte("dump res failed")
	}
	data["dump"] = base64.StdEncoding.EncodeToString(res_dump)
	//   data["cookies"]=res.Cookies()

	body := []byte("pproxy skip")
	if res.ContentLength <= ser.MaxResSaveLength {
		buf := forgetRead(&res.Body)
		body = buf.Bytes()
	}
	data["body"] = base64.StdEncoding.EncodeToString(body)

	err := ser.mydb.ResponseTable.InsertRecovery(req_uid, data)
	log.Println("save_res [", req_uid, "]", err)
	if err != nil {
		log.Println(err)
		return
	}
}

func (ser *ProxyServe) GetResponseByDocid(docid uint64) (res_data kvType) {
	id, err := ser.mydb.ResponseTable.Read(docid, &res_data)
	if err != nil {
		log.Println("read res by docid failed,docid=", docid, "id=", id, err)
	}
	//  fmt.Println(docid,res_data)
	return res_data
}
func (ser *ProxyServe) GetRequestByDocid(docid uint64) (req_data kvType) {
	id, err := ser.mydb.RequestTable.Read(docid, &req_data)
	if err != nil {
		log.Println("read req by docid failed,docid=", docid, "id=", id, err)
	}
	return req_data
}

func NewProxyServe(data_dir string,jsPath string,port int) *ProxyServe {
	proxy := new(ProxyServe)
	proxy.Port=port
	proxy.mydb = NewTieDb(fmt.Sprintf("%s/%d/",data_dir,port))
	proxy.startTime = time.Now()
	proxy.MaxResSaveLength = 2 * 1024 * 1024
	
	script, err:= ioutil.ReadFile(jsPath)
	if(err==nil){
	   proxy.RewriteJs=string(script)
		js= otto.New()
	   js.Run(proxy.RewriteJs)
	   jsFn,_=js.Get("filter")
	   log.Println("create jsFn:",jsFn)
	}
   rand.Seed(time.Now().UnixNano())
   
//   proxy.mydb.StartGcTimer(60,store_time)
	return proxy
}

