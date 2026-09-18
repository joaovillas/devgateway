package app

import (
	"os"

	"github.com/gamerjp64/gateway/internal/config"
)

func loaderFor(path string) config.Loader {
	return config.Loader{ConfigPath: path, Getenv: os.LookupEnv}
}
