// Package webui несёт собранный веб-клиент, вшитый в бинарник сервера.
//
// Каталог dist наполняет сборка фронта (make web). До неё внутри лежит
// только заглушка, и сервер поднимается без веб-клиента — API работает.
package webui

import "embed"

//go:embed all:dist
var Dist embed.FS
