package common

import "github.com/anchel/rathole-go/internal/config"

type ResponseDataHello struct {
	Ok  bool
	Typ config.ServiceType
}
