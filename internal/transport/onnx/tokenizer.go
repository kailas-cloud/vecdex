package onnx

import hftokenizer "github.com/sugarme/tokenizer"

type textTokenizer interface {
	TokenToId(token string) (int, bool)
	WithTruncation(params *hftokenizer.TruncationParams)
	WithPadding(params *hftokenizer.PaddingParams)
	EncodeBatch(inputs []hftokenizer.EncodeInput, addSpecialTokens bool) ([]hftokenizer.Encoding, error)
}
