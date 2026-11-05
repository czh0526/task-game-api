// Command token mints a dev HS256 JWT for the configured secret.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"taskgame/internal/auth"
	"taskgame/internal/config"

	"github.com/zeromicro/go-zero/core/conf"
)

func main() {
	file := flag.String("f", "etc/taskgame.yaml", "config file")
	user := flag.String("user", "usr_publisher", "subject user id")
	ttl := flag.Duration("ttl", 24*time.Hour, "token lifetime")
	flag.Parse()

	var c config.Config
	if err := conf.Load(*file, &c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	signed, err := auth.SignHS256(c.Auth.JwtSecret, *user, *ttl, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(signed)
}
