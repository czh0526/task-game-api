package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"

	"taskgame/internal/biz"
	"taskgame/internal/config"
	"taskgame/internal/handler"
	"taskgame/internal/svc"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/httpx"
)

var configFile = flag.String("f", "etc/taskgame.yaml", "the config file")

func main() {
	flag.Parse()

	var c config.Config
	conf.MustLoad(*configFile, &c)

	httpx.SetErrorHandlerCtx(func(ctx context.Context, err error) (int, any) {
		var be *biz.Error
		if errors.As(err, &be) {
			return be.Status, be.Body()
		}
		logx.WithContext(ctx).Errorf("internal: %v", err)
		return http.StatusInternalServerError, map[string]string{
			"code":    "internal",
			"message": "internal error",
		}
	})

	server := rest.MustNewServer(c.RestConf)
	defer server.Stop()

	sc, err := svc.NewServiceContext(c)
	if err != nil {
		logx.Must(err)
	}
	defer sc.Close()

	handler.RegisterHandlers(server, sc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc.StartBackground(ctx)

	fmt.Printf("taskgame listening on http://%s:%d (health /health, api /v1)\n", c.Host, c.Port)
	server.Start()
	cancel()
}
