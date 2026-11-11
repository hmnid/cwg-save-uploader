package app

import "log"

func componentLogger(base *log.Logger, name string) *log.Logger {
	return log.New(base.Writer(), base.Prefix()+"["+name+"] ", base.Flags())
}
