package utils

import "runtime"

type PathType int32

var P PathType

const (
	PathGet PathType = iota
)

var Path PathType

func GetCurrentFunctionName() string {
	pc, _, _, _ := runtime.Caller(1)
	return runtime.FuncForPC(pc).Name()
}
