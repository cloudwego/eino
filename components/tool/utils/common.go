package utils

import (
	"github.com/bytedance/sonic"
)

func marshalString(resp any) (string, error) {
	if rs, ok := resp.(string); ok {
		return rs, nil
	}
	return sonic.MarshalString(resp)
}
