/**
 */

package goku

import (
    "bytes"
    // "errors"
    "fmt"
    "net/http"
    "path"
    "time"
    //"log"
    "runtime/debug"
)

// all the config to the web server
type ServerConfig struct {
    Addr           string        // TCP address to listen on, ":http" if empty
    ReadTimeout    time.Duration // maximum duration before timing out read of the request
    WriteTimeout   time.Duration // maximum duration before timing out write of the response
    MaxHeaderBytes int           // maximum size of request headers, DefaultMaxHeaderBytes if 0

    RootDir    string // project root dir
    StaticPath string // static file dir, "static" if empty
    ViewPath   string // view file dir, "views" if empty
    Layout     string // template layout, "layout" if empty

    ViewEnginer     ViewEnginer
    TemplateEnginer TemplateEnginer

    Debug bool
}

// server inherit from http.Server
type Server struct {
    http.Server
}

// request handler, the main handler for all the requests
type RequestHandler struct {
    RouteTable        *RouteTable
    MiddlewareHandler MiddlewareHandler
    ServerConfig      *ServerConfig
    ViewEnginer       ViewEnginer
}

// implement the http.Handler interface
// the main entrance of the request handler
func (rh *RequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    var ctx *HttpContext
    ctx = rh.buildContext(w, r)
    var (
        ar  ActionResulter
        err error
    )
    ar, err = rh.execute(ctx)
    if err != nil {
        ar = ctx.Error(err)
    }
    if ar != nil {
        ar.ExecuteResult(ctx)
    }
    // response content was cached,
    // flush all the cached content to responsewriter
    ctx.flushToResponse()
    logRequestInfo(ctx)
}

// 你可以通过三种途径取消一个请求： 设置 ctx.Canceled = true , 返回一个ActionResulter或者一个错误
func (rh *RequestHandler) execute(ctx *HttpContext) (ar ActionResulter, err error) {
    defer func() {
        // handle all the error
        err_ := recover()
        if err_ == nil {
            return
        }

        der := &devErrorResult{
            StatusCode: 500,
            Err:        fmt.Sprintf("%v", err_),
            ShowDetail: rh.ServerConfig.Debug, // if debug enable, show detail
        }
        if Logger().LogLevel() >= LOG_LEVEL_ERROR || der.ShowDetail {
            var buf bytes.Buffer
            buf.Write(debug.Stack())
            if der.ShowDetail {
                der.Stack = buf.String()
            }
            Logger().Errorln(der.Err, "\n", buf.String())
        }

        err = nil
        ar = der
        return
    }()

    // being request
    ar, err = rh.MiddlewareHandler.BeginRequest(ctx)
    if ctx.Canceled || err != nil || ar != nil {
        return
    }
    // match route
    routeData, ok := rh.RouteTable.Match(ctx.Request.URL.Path)
    if !ok {
        ar = ctx.NotFound("Page Not Found! No Route For The URL: " + ctx.Request.URL.Path)
        return
    }
    ctx.RouteData = routeData
    // static file route
    // return ContentResult
    if routeData.Route.IsStatic {
        sc := ctx.requestHandler.ServerConfig
        filePath := path.Join(sc.RootDir, sc.StaticPath, routeData.FilePath)
        //fmt.Printf("fp: %s\n", filePath)
        ar = &ContentResult{
            FilePath: filePath,
        }
        ar.ExecuteResult(ctx)
    } else {
        // parse form data before mvc handle
        ctx.Request.ParseForm()
        // begin mvc handle
        ar, err = rh.MiddlewareHandler.BeginMvcHandle(ctx)
        if ctx.Canceled || err != nil || ar != nil {
            return
        }
        // handle controller
        ar, err = rh.executeController(ctx, ctx.RouteData.Controller, ctx.RouteData.Action)
        if ctx.Canceled || err != nil || ar != nil {
            return
        }
        // end mvc handle
        ar, err = rh.MiddlewareHandler.EndMvcHandle(ctx)
        if ctx.Canceled || err != nil || ar != nil {
            return
        }
    }
    // end request
    ar, err = rh.MiddlewareHandler.EndRequest(ctx)
    return
}

// execute controller,action,and filter
func (rh *RequestHandler) executeController(ctx *HttpContext, controller, action string) (ar ActionResulter, err error) {
    var ai *ActionInfo
    ai = defaultControllerFactory.GetAction(ctx.Method, controller, action)
    if ai == nil {
        ar = ctx.NotFound(fmt.Sprintf("No [%v] Action For {Controlle:%s, Action:%s}.",
            ctx.Method, controller, action))
        return
    }
    // ing & ed filter's order is not the same
    ingFilters := append(ai.Controller.Filters, ai.Filters...)
    // action executing filter
    ar, err = runFilterActionExecuting(ctx, ingFilters)
    if ctx.Canceled || err != nil || ar != nil {
        return
    }
    // execute action
    var rar ActionResulter
    rar = ai.Handler(ctx)
    // action executed filter
    edFilters := append(ai.Filters, ai.Controller.Filters...)
    ar, err = runFilterActionExecuted(ctx, edFilters)
    if ctx.Canceled || err != nil || ar != nil {
        return
    }
    // resule executing filter
    ar, err = runFilterResultExecuting(ctx, ingFilters)
    if ctx.Canceled || err != nil || ar != nil {
        return
    }
    // execute action result
    rar.ExecuteResult(ctx)
    // result executed filter
    ar, err = runFilterResultExecuted(ctx, edFilters)
    return
}

func (rh *RequestHandler) buildContext(w http.ResponseWriter, r *http.Request) *HttpContext {
    //r.ParseForm()
    return &HttpContext{
        Request:              r,
        responseWriter:       w,
        Method:               r.Method,
        requestHandler:       rh,
        ViewData:             make(map[string]interface{}),
        responseContentCache: new(bytes.Buffer),
        //responseHeaderCache: make(map[string]string),
    }
}

func logRequestInfo(ctx *HttpContext) {
    if Logger().LogLevel() < LOG_LEVEL_LOG {
        return
    }
    status := 200
    if ctx.responseStatusCode > 0 {
        status = ctx.responseStatusCode
    }
    routeInfo := ""
    // N: Unknown
    // D: Dynamic request
    // S: Static file
    handleType := "N"
    if ctx.RouteData != nil {
        handleType = "D"
        if ctx.RouteData.Route.IsStatic {
            handleType = "S"
        }
        //routeInfo = fmt.Sprintf(" >>[n:%v, p:%v]", ctx.RouteData.Route.Name, ctx.RouteData.Route.Pattern)
    }
    Logger().Logln(handleType, status, ctx.Request.Method, ctx.Request.RequestURI, routeInfo)
}

// func (rh *RequestHandler) checkError(ctx *HttpContext, ar ActionResulter, err error) ActionResulter {
//     if err != nil {
//         return ctx.Error(err)
//     }
//     return ar
// }

// create a server to handle the request
// routeTable is about the rule map a url to a controller action
// middlewares are the way you can process request during handle request
// sc is the config how the server work
func CreateServer(routeTable *RouteTable, middlewares []Middlewarer, sc *ServerConfig) *Server {
    if sc.RootDir == "" {
        panic("gokuServer: Root Dir must set")
    }
    if routeTable == nil {
        panic("gokuServer: RouteTable is nil")
    }
    if routeTable.Routes == nil || len(routeTable.Routes) < 1 {
        panic("gokuServer: No Route in the RouteTable")
    }

    mh := &DefaultMiddlewareHandle{
        Middlewares: middlewares,
    }

    handler := &RequestHandler{
        RouteTable:        routeTable,
        MiddlewareHandler: mh,
        ServerConfig:      sc,
        ViewEnginer:       sc.ViewEnginer,
    }
    if sc.ViewPath == "" {
        sc.ViewPath = "views"
    }
    // default view engine
    if handler.ViewEnginer == nil {
        handler.ViewEnginer = CreateDefaultViewEngine(
            path.Join(sc.RootDir, sc.ViewPath),
            sc.TemplateEnginer,
            sc.Layout,
            !sc.Debug, // cache template
        )
    }

    server := new(Server)
    server.Handler = handler
    server.Addr = sc.Addr
    server.ReadTimeout = sc.ReadTimeout
    server.WriteTimeout = sc.WriteTimeout
    server.MaxHeaderBytes = sc.MaxHeaderBytes
    return server
}
