#!/bin/sh

mkdir -p build

go build -o ./build/alerter ./alerter
go build -o ./build/alerter-db-loader ./alerter-db-loader
go build -o ./build/alerter-viewer ./alerter-viewer

go build -o ./build/detector ./detector
go build -o ./build/detector-viewer ./detector-viewer

go build -o ./build/recognizer ./recognizer
go build -o ./build/recognizer-viewer ./recognizer-viewer

go build -o ./build/telegramer ./telegramer