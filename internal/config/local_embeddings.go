package config

import (
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedlocal"
)

// LocalEmbeddingRecipe supplies a temporary URL for an endpointless recipe.
func LocalEmbeddingRecipe(e embedconfig.Embedder) embedconfig.Embedder {
	e.BaseURL = "http://127.0.0.1:1/v1"
	return e
}

// LocalEmbeddingCompatible reports whether the built-in server can serve the recipe.
func LocalEmbeddingCompatible(e embedconfig.Embedder) bool {
	parts, err := e.Parts()
	if err != nil {
		return false
	}
	m := parts.Model
	return (m.Name == embedlocal.TextModel || m.Name == "nomic-embed-text:latest" || m.Name == "nomic-embed-text:v1.5") && m.Revision == "" && m.Dimensions <= 768 && (m.Dimensions == 768 || m.RequestDimensions)
}
