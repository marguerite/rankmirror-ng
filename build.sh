#!/bin/bash

mkdir -p rankmirror-ng.AppDir/usr/bin
go build -o rankmirror-ng.AppDir/usr/bin/rankmirror-ng
cp mirrors.yaml rankmirror-ng.AppDir/usr
wget https://github.com/AppImage/AppImageKit/releases/download/12/appimagetool-x86_64.AppImage
chmod +x appimagetool-x86_64.AppImage
wget https://github.com/AppImage/AppImageKit/releases/download/12/AppRun-x86_64
cp -r AppRun-x86_64 rankmirror-ng.AppDir/AppRun
chmod +x rankmirror-ng.AppDir/AppRun
cp -r rankmirror-ng.desktop rankmirror-ng.AppDir
cp -r yast-services-manager.svg rankmirror-ng.AppDir
./appimagetool-x86_64.AppImage rankmirror-ng.AppDir
