#!/bin/bash

targetOs="linux"
if [[ $# == 1 ]]
then
    targetOs=$1 
fi
echo "build a executable for $targetOs paltform amd64"
env GOOS=$targetOs GOARCH=amd64 go build
