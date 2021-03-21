package main

import "testing"

func TestReplaceMirror(t *testing.T) {
	str := "https://download.opensuse.org/tumbleweed/repo/oss"
	str1 := "http://mirrors.tuna.tsinghua.edu.cn/opensuse/"
	s := replaceMirror(str, str1)
	s1 := "http://mirrors.tuna.tsinghua.edu.cn/opensuse/tumbleweed/repo/oss"
	if s != s1 {
		t.Errorf("replaceMirror failed, expected %s, got %s", s1, s)
	}
}
