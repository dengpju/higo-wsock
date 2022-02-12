package wsock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"gitee.com/dengpju/higo-code/code"
	"github.com/dengpju/higo-logger/logger"
	"github.com/dengpju/higo-router/router"
	"github.com/dengpju/higo-throw/exception"
	"github.com/dengpju/higo-utils/utils/maputil"
	"github.com/dengpju/higo-utils/utils/runtimeutil"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"net/http"
	"sync"
	"time"
)

var (
	//Recover处理函数(可自定义替换)
	WsRecoverHandle WsRecoverFunc
	wsRecoverOnce   sync.Once
)

func init() {
	wsRecoverOnce.Do(func() {
		WsRecoverHandle = func(r interface{}) (respMsg string) {
			if msg, ok := r.(*code.CodeMessage); ok {
				respMsg = maputil.Array().
					Put("code", msg.Code).
					Put("message", msg.Message).
					Put("data", nil).
					String()
			} else if arrayMap, ok := r.(maputil.ArrayMap); ok {
				respMsg = arrayMap.String()
			} else {
				respMsg = maputil.Array().
					Put("code", 0).
					Put("message", exception.ErrorToString(r)).
					Put("data", nil).
					String()
			}
			return
		}
	})
}

type WsRecoverFunc func(r interface{}) string

type WebsocketConn struct {
	ctx          *gin.Context
	route        *router.Route
	conn         *websocket.Conn
	readChan     chan *WsReadMessage
	writeChan    chan WsWriteMessage
	dispatchChan chan WsWriteMessage
	closeChan    chan byte
}

func NewWebsocketConn(ctx *gin.Context, route *router.Route, conn *websocket.Conn) *WebsocketConn {
	return &WebsocketConn{ctx: ctx, route: route, conn: conn, readChan: make(chan *WsReadMessage),
		writeChan: make(chan WsWriteMessage), dispatchChan: make(chan WsWriteMessage), closeChan: make(chan byte)}
}

func (this *WebsocketConn) Conn() *websocket.Conn {
	return this.conn
}

func (this *WebsocketConn) Close() {
	err := this.conn.Close()
	if err != nil {
		panic(err)
	}
	WsContainer.Remove(this.conn)
	this.closeChan <- 1
}

func (this *WebsocketConn) ping(waittime time.Duration) {
	for {
		WsPingHandle(this, waittime)
	}
}

func (this *WebsocketConn) readLoop() {
	for {
		t, message, err := this.conn.ReadMessage()
		if err != nil {
			this.Close()
			break
		}
		this.readChan <- NewReadMessage(t, message)
	}
}

func (this *WebsocketConn) writeLoop() {
loop:
	for {
		select {
		case msg := <-this.writeChan:
			if WsResperror == msg.MessageType {
				_ = this.conn.WriteMessage(websocket.TextMessage, msg.MessageData)
				this.Close()
				break loop
			}
			if err := this.conn.WriteMessage(websocket.TextMessage, msg.MessageData); err != nil {
				this.Close()
				break loop
			}
		}
	}
}

func (this *WebsocketConn) handlerLoop() {
	defer func() {
		if r := recover(); r != nil {
			logger.LoggerStack(r, runtimeutil.GoroutineID())
			this.writeChan <- WsRespError(WsRecoverHandle(r))
		}
	}()
loop:
	for {
		select {
		case msg := <-this.readChan:
			// 写数据
			this.writeChan <- this.dispatch(msg)
		case <-this.closeChan:
			logger.Logrus.Info("websocket conn " + this.Conn().RemoteAddr().String() + " have already closed")
			break loop
		}
	}
}

func (this *WebsocketConn) dispatch(msg *WsReadMessage) WsWriteMessage {
	handle := this.route.Handle()
	ctx := this.ctx
	reader := bytes.NewReader(msg.MessageData)
	request, err := http.NewRequest(router.POST, this.route.AbsolutePath(), reader)
	if err != nil {
		panic(err)
	}
	request.Header.Set("Content-Type", "application/json")
	ctx.Request = request
	handle.(gin.HandlerFunc)(ctx)
	fmt.Println(111)
	return <-this.dispatchChan
}

func (this *WebsocketConn) WriteMessage(message string) {
	err := this.Conn().WriteMessage(websocket.TextMessage, []byte(message))
	if err != nil {
		panic(err)
	}
}

func (this *WebsocketConn) WriteMap(message maputil.ArrayMap) {
	err := this.Conn().WriteMessage(websocket.TextMessage, []byte(message.String()))
	if err != nil {
		panic(err)
	}
}

func (this *WebsocketConn) WriteStruct(message interface{}) {
	go func(msg interface{}) {
		this.dispatchChan <- WsRespStruct(msg)
	}(message)
}

func (this *WebsocketConn) Error(message interface{}) {
	mjson, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	err = this.Conn().WriteMessage(websocket.TextMessage, mjson)
	if err != nil {
		panic(err)
	}
}

func Response(ctx *gin.Context) *WebsocketConn {
	return WsConn(ctx)
}

func WsConn(ctx *gin.Context) *WebsocketConn {
	client, ok := ctx.Get(WsConnIp)
	if !ok {
		panic("websocket conn client non-existent")
	}
	if conn, ok := WsContainer.clients.Load(client); ok {
		return conn.(*WebsocketConn)
	} else {
		panic("websocket conn non-existent")
	}
}

//webSocket请求连接
func websocketConnFunc(ctx *gin.Context) string {
	//升级get请求为webSocket协议
	client, err := Upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
	if err != nil {
		panic(err)
	}

	route := router.GetRoutes(WebsocketServe).Route(ctx.Request.Method, ctx.Request.URL.Path).SetHeader(ctx.Request.Header)

	WsContainer.Store(ctx, route, client)
	return client.RemoteAddr().String()
}

func wsPingFunc(websocketConn *WebsocketConn, waittime time.Duration) {
	time.Sleep(waittime)
	err := websocketConn.conn.WriteMessage(websocket.TextMessage, []byte("ping"))
	if err != nil {
		WsContainer.Remove(websocketConn.conn)
		return
	}
}
