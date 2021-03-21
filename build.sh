#!/bin/bash

go mod download
go mod vendor
go build -o rankmirror-ng.AppDir/usr/bin/rankmirror-ng
cp mirrors.yaml rankmirror-ng.AppDir/usr
wget https://github.com/AppImage/AppImageKit/releases/download/12/appimagetool-x86_64.AppImage
chmod +x appimagetool-x86_64.AppImage
wget https://github.com/AppImage/AppImageKit/releases/download/12/AppRun-x86_64
mv AppRun-x86_64 rankmirror-ng.AppDir/AppRun
./appimagetool-x86_64.AppImage rankmirror-ng.AppDir
mv rankmirror-ng-x86_64.AppImage rankmirror-ng-v2-1.0.0.x86_64.AppImage
