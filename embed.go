package main

import "embed"

//go:embed templates/*
var TemplatesFS embed.FS

//go:embed static/css/* static/js/* static/logo.png
var StaticFS embed.FS

//go:embed wp-panel-optimizer/*
var PluginFS embed.FS
