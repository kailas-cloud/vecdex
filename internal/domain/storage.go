package domain

// KeyPrefix is the common Valkey key prefix used by all repositories.
const KeyPrefix = "vecdex:"

// CollectionBase separates schema versions and assigns one cluster slot per collection.
func CollectionBase(name string) string { return KeyPrefix + "v2:{" + name + "}" }

// CollectionMetadataKey identifies a collection's metadata hash.
func CollectionMetadataKey(name string) string { return CollectionBase(name) + ":meta" }

// DocumentPrefix is the prefix indexed for documents in a collection.
func DocumentPrefix(name string) string { return CollectionBase(name) + ":doc:" }

// CollectionIndexName identifies the collection search index.
func CollectionIndexName(name string) string { return CollectionBase(name) + ":index" }

// CollectionDataBase isolates records belonging to one incarnation of a collection.
func CollectionDataBase(name, generation string) string {
	if generation == "" {
		return CollectionBase(name)
	}
	return CollectionBase(name) + ":g:" + generation
}

// GenerationDocumentPrefix selects documents of a particular collection incarnation.
func GenerationDocumentPrefix(name, generation string) string {
	return CollectionDataBase(name, generation) + ":doc:"
}

// GenerationIndexName selects the search index of a particular collection incarnation.
func GenerationIndexName(name, generation string) string {
	return CollectionDataBase(name, generation) + ":index"
}

// DocumentCatalogKey stores only source IDs for one collection incarnation.
func DocumentCatalogKey(name, generation string) string {
	return CollectionDataBase(name, generation) + ":document-ids"
}
