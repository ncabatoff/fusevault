package main

import (
	"log"

	"github.com/hashicorp/vault/api"
)

type vaultapi struct {
	*api.Client
}

func (v vaultapi) Logical() *vaultlog {
	return &vaultlog{v.Client.Logical()}
}

type vaultlog struct {
	*api.Logical
}

func (c *vaultlog) Delete(path string) (*api.Secret, error) {
	if debug {
		log.Printf("Delete(%s)\n", path)
	}
	return c.Logical.Delete(path)

}
func (c *vaultlog) List(path string) (*api.Secret, error) {
	if debug {
		log.Printf("List(%s)\n", path)
	}
	return c.Logical.List(path)

}
func (c *vaultlog) Read(path string) (*api.Secret, error) {
	if debug {
		log.Printf("Read(%s)\n", path)
	}
	return c.Logical.Read(path)

}
func (c *vaultlog) ReadWithData(path string, data map[string][]string) (*api.Secret, error) {
	if debug {
		log.Printf("Write(%s, %v)\n", path, data)
	}
	return c.Logical.ReadWithData(path, data)

}
func (c *vaultlog) Unwrap(wrappingToken string) (*api.Secret, error) {
	if debug {
		log.Printf("Unwrap(%s)\n", wrappingToken)
	}
	return c.Logical.Unwrap(wrappingToken)

}
func (c *vaultlog) Write(path string, data map[string]interface{}) (*api.Secret, error) {
	if debug {
		log.Printf("Write(%s, %v)\n", path, data)
	}
	return c.Logical.Write(path, data)
}
