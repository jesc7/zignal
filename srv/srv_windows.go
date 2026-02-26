package srv

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

func runPath(service bool) (string, error) {
	res := os.Args[0]
	if service {
		key, e := registry.OpenKey(registry.LOCAL_MACHINE, "SYSTEM\\CurrentControlSet\\Services\\zignal", registry.QUERY_VALUE)
		if e != nil {
			return "", e
		}
		if res, _, e = key.GetStringValue("ImagePath"); e != nil {
			return "", e
		}
	}
	return res, nil
}
