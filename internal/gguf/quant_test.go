package gguf

import "testing"

func TestQuantFromName(t *testing.T) {
	cases := map[string]string{
		"/models/LFM2.5-8B-A1B-Q4_K_M.gguf":                        "Q4_K_M",
		"/models/gemma-4-26B-A4B-it-UD-Q4_K_S.gguf":                "Q4_K_S",
		"/models/gemma-4-26B-A4B-it-qat-UD-Q4_K_XL.gguf":           "Q4_K_XL",
		"/models/Qwen3.8-Flash-Next-UD-IQ4_XS-00001-of-00003.gguf": "IQ4_XS",
		"/models/Bonsai-27B-Q1_0.gguf":                             "Q1_0",
		"/models/mmproj-F32.gguf":                                  "F32",
		"/models/mmproj-Bonsai-27B-BF16.gguf":                      "BF16",
		"/models/qwen2.5-coder-7b-instruct-q5_k_m.gguf":            "Q5_K_M",
		"/models/model-00002-of-00003.gguf":                        "",
		"/models/Qwen3-4B-Instruct-2507-Q6_K.gguf":                 "Q6_K",
		"/models/gpt-oss-20b-MXFP4.gguf":                           "MXFP4",
		"/models/gpt-oss-120b-MXFP4_MOE.gguf":                      "MXFP4_MOE",
		"/models/Qwen3.8-27B-UD-IQ3_XXS.gguf":                      "IQ3_XXS",
		"/models/llama-3.1-8b-instruct-ud-iq1_s.gguf":              "IQ1_S",
	}
	for path, want := range cases {
		if got := QuantFromName(path); got != want {
			t.Errorf("QuantFromName(%q) = %q, want %q", path, got, want)
		}
	}
}
