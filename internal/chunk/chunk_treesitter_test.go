//go:build treesitter

package chunk

import (
	"fmt"
	"strings"
	"testing"
)

func TestTsChunk_Python_Basic(t *testing.T) {
	var lines []string
	lines = append(lines, "import os")
	lines = append(lines, "import sys")
	lines = append(lines, "")
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("def function_%d():", i))
		for j := 0; j < 10; j++ {
			lines = append(lines, fmt.Sprintf("    print(\"line %d from function %d\")", j, i))
		}
		lines = append(lines, "")
	}
	src := strings.Join(lines, "\n")

	chunks := (&PythonChunker{}).Split(src, 50, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "import os") {
		t.Errorf("expected preamble with imports in chunks")
	}
	if !strings.Contains(joined, "def function_0():") {
		t.Errorf("expected function_0 in chunks")
	}
	if !strings.Contains(joined, "def function_4():") {
		t.Errorf("expected function_4 in chunks")
	}
}

func TestTsChunk_Python_Class(t *testing.T) {
	src := `import os

class MyClass:
    def __init__(self):
        self.x = 1

    def method(self):
        return self.x

def standalone():
    pass
`
	chunks := (&PythonChunker{}).Split(src, 50, 0.15)
	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk, got %d", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "class MyClass:") {
		t.Errorf("expected class in chunks")
	}
	if !strings.Contains(joined, "def standalone():") {
		t.Errorf("expected standalone function in chunks")
	}
}

func TestTsChunk_Python_Decorated(t *testing.T) {
	src := `from functools import wraps

@wraps
def decorated_func():
    pass

class Foo:
    @property
    def bar(self):
        return 1
`
	chunks := (&PythonChunker{}).Split(src, 50, 0.15)
	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk, got %d", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "@wraps") {
		t.Errorf("expected decorator in chunks")
	}
	if !strings.Contains(joined, "def decorated_func():") {
		t.Errorf("expected decorated function in chunks")
	}
}

func TestTsChunk_Python_Invalid(t *testing.T) {
	chunks := (&PythonChunker{}).Split("this is not valid @#$%", 100, 0.15)
	if chunks != nil {
		t.Fatalf("expected nil for invalid Python, got %d chunks", len(chunks))
	}
}

func TestTsChunk_JavaScript_Basic(t *testing.T) {
	src := `import React from 'react';
import { useState } from 'react';

function Component() {
  const [count, setCount] = useState(0);
  return count;
}

export default Component;

class MyClass {
  constructor() {
    this.x = 1;
  }
  method() {
    return this.x;
  }
}

const arrow = () => 42;
`
	chunks := (&JavaScriptChunker{}).Split(src, 20, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "function Component()") {
		t.Errorf("expected Component function in chunks")
	}
	if !strings.Contains(joined, "class MyClass") {
		t.Errorf("expected MyClass in chunks")
	}
}

func TestTsChunk_JavaScript_Invalid(t *testing.T) {
	chunks := (&JavaScriptChunker{}).Split("@#$%^&*()", 100, 0.15)
	if chunks != nil {
		t.Fatalf("expected nil for invalid JS, got %d chunks", len(chunks))
	}
}

func TestTsChunk_TypeScript_Basic(t *testing.T) {
	src := `import { Request, Response } from 'express';

interface User {
  id: number;
  name: string;
}

type UserId = number;

function getUser(id: number): User {
  return { id, name: "test" };
}

export default getUser;
`
	chunks := (&TypeScriptChunker{}).Split(src, 20, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "interface User") {
		t.Errorf("expected interface in chunks")
	}
	if !strings.Contains(joined, "type UserId") {
		t.Errorf("expected type alias in chunks")
	}
}

func TestTsChunk_Rust_Basic(t *testing.T) {
	src := `use std::collections::HashMap;

fn main() {
    println!("hello");
}

pub struct Config {
    verbose: bool,
    paths: Vec<String>,
}

impl Config {
    pub fn new() -> Self {
        Self { verbose: false, paths: vec![] }
    }
}

enum Result {
    Ok,
    Err(String),
}

const MAX_SIZE: usize = 1024;
`
	chunks := (&RustChunker{}).Split(src, 50, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "fn main()") {
		t.Errorf("expected main function in chunks")
	}
	if !strings.Contains(joined, "pub struct Config") {
		t.Errorf("expected struct in chunks")
	}
	if !strings.Contains(joined, "impl Config") {
		t.Errorf("expected impl block in chunks")
	}
}

func TestTsChunk_Rust_Invalid(t *testing.T) {
	chunks := (&RustChunker{}).Split("@#$%^&*()", 100, 0.15)
	if chunks != nil {
		t.Fatalf("expected nil for invalid Rust, got %d chunks", len(chunks))
	}
}

func TestTsChunk_Java_Basic(t *testing.T) {
	src := `package com.example;

import java.util.List;
import java.util.ArrayList;

public class Calculator {
    private int result;

    public Calculator() {
        this.result = 0;
    }

    public int add(int a, int b) {
        return a + b;
    }
}

interface Operation {
    int apply(int a, int b);
}
`
	chunks := (&JavaChunker{}).Split(src, 20, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "package com.example") {
		t.Errorf("expected package declaration in chunks")
	}
	if !strings.Contains(joined, "public class Calculator") {
		t.Errorf("expected class in chunks")
	}
	if !strings.Contains(joined, "interface Operation") {
		t.Errorf("expected interface in chunks")
	}
}

func TestTsChunk_C_Basic(t *testing.T) {
	src := `#include <stdio.h>
#include <stdlib.h>

int add(int a, int b) {
    return a + b;
}

void print_hello(void) {
    printf("hello\\n");
}

struct Point {
    int x;
    int y;
};

typedef struct Point Point;
`
	chunks := (&CChunker{}).Split(src, 20, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "int add") {
		t.Errorf("expected add function in chunks")
	}
	if !strings.Contains(joined, "struct Point") {
		t.Errorf("expected struct in chunks")
	}
}

func TestTsChunk_CPP_Basic(t *testing.T) {
	src := `#include <iostream>
#include <vector>

class Shape {
public:
    virtual double area() const = 0;
    virtual ~Shape() = default;
};

class Circle : public Shape {
    double radius;
public:
    Circle(double r) : radius(r) {}
    double area() const override { return 3.14159 * radius * radius; }
};

namespace geometry {
    double compute_total_area(const std::vector<Shape*>& shapes) {
        double total = 0;
        for (auto* s : shapes) total += s->area();
        return total;
    }
}
`
	chunks := (&CPPChunker{}).Split(src, 50, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	joined := ""
	for _, c := range chunks {
		joined += c.Content
	}
	if !strings.Contains(joined, "class Shape") {
		t.Errorf("expected Shape class in chunks")
	}
	if !strings.Contains(joined, "namespace geometry") {
		t.Errorf("expected namespace in chunks")
	}
}

func TestTsChunk_LargeDeclaration(t *testing.T) {
	lines := []string{"import os", ""}
	lines = append(lines, "def big_function():")
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("    x = %d # %s", i, strings.Repeat("word ", 20)))
	}
	lines = append(lines, "")
	lines = append(lines, "def small_function():")
	lines = append(lines, "    pass")
	src := strings.Join(lines, "\n")

	chunks := (&PythonChunker{}).Split(src, 10, 0.15)
	if len(chunks) < 2 {
		t.Fatalf("expected large function to be split, got %d chunks", len(chunks))
	}
}

func TestTsChunk_MergesSmallDeclarations(t *testing.T) {
	src := `import os

def a():
    pass

def b():
    pass

def c():
    pass
`
	chunks := (&PythonChunker{}).Split(src, 100, 0.15)
	if len(chunks) < 1 {
		t.Fatalf("expected at least 1 chunk, got %d", len(chunks))
	}
}

func TestTsSignature(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"func main() {}", "func main() {}"},
		{"  \nfunc main() {}", "func main() {}"},
		{strings.Repeat("x", 150), strings.Repeat("x", 120) + "..."},
		{"", ""},
	}
	for _, tt := range tests {
		got := tsSignature(tt.input)
		if got != tt.want {
			t.Errorf("tsSignature(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTsChunker_Priorities(t *testing.T) {
	if (&PythonChunker{}).Priority() != 75 {
		t.Errorf("PythonChunker priority should be 75")
	}
	if (&JavaScriptChunker{}).Priority() != 75 {
		t.Errorf("JavaScriptChunker priority should be 75")
	}
	if (&TypeScriptChunker{}).Priority() != 75 {
		t.Errorf("TypeScriptChunker priority should be 75")
	}
	if (&TSXChunker{}).Priority() != 75 {
		t.Errorf("TSXChunker priority should be 75")
	}
	if (&RustChunker{}).Priority() != 75 {
		t.Errorf("RustChunker priority should be 75")
	}
	if (&JavaChunker{}).Priority() != 75 {
		t.Errorf("JavaChunker priority should be 75")
	}
	if (&KotlinChunker{}).Priority() != 75 {
		t.Errorf("KotlinChunker priority should be 75")
	}
	if (&CChunker{}).Priority() != 75 {
		t.Errorf("CChunker priority should be 75")
	}
	if (&CPPChunker{}).Priority() != 75 {
		t.Errorf("CPPChunker priority should be 75")
	}
}

func TestTsChunker_Supports(t *testing.T) {
	tests := []struct {
		chunker Chunker
		path    string
		want    bool
	}{
		{&PythonChunker{}, "test.py", true},
		{&PythonChunker{}, "test.pyw", true},
		{&PythonChunker{}, "test.js", false},
		{&JavaScriptChunker{}, "test.js", true},
		{&JavaScriptChunker{}, "test.jsx", true},
		{&JavaScriptChunker{}, "test.mjs", true},
		{&JavaScriptChunker{}, "test.ts", false},
		{&TypeScriptChunker{}, "test.ts", true},
		{&TypeScriptChunker{}, "test.tsx", false},
		{&TypeScriptChunker{}, "test.js", false},
		{&TSXChunker{}, "test.tsx", true},
		{&TSXChunker{}, "test.ts", false},
		{&KotlinChunker{}, "test.kt", true},
		{&KotlinChunker{}, "test.kts", true},
		{&KotlinChunker{}, "test.java", false},
		{&RustChunker{}, "test.rs", true},
		{&RustChunker{}, "test.go", false},
		{&JavaChunker{}, "Test.java", true},
		{&JavaChunker{}, "test.kt", false},
		{&CChunker{}, "test.c", true},
		{&CChunker{}, "test.h", true},
		{&CChunker{}, "test.cpp", false},
		{&CPPChunker{}, "test.cpp", true},
		{&CPPChunker{}, "test.cc", true},
		{&CPPChunker{}, "test.c", false},
	}
	for _, tt := range tests {
		got := tt.chunker.Supports(tt.path)
		if got != tt.want {
			t.Errorf("%s.Supports(%q) = %v, want %v", tt.chunker.Name(), tt.path, got, tt.want)
		}
	}
}
