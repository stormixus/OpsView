package lpr

import (
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config holds ONNX LPR paths and thresholds.
type Config struct {
	Enabled       bool
	ORTLib        string
	DetectorONNX  string
	OCRONNX       string
	OCRConfigYAML string
	DetConf       float32
	MinConf       float64
}

// PlateConfig mirrors fast-plate-ocr plate_config.yaml (inference subset).
type PlateConfig struct {
	MaxPlateSlots   int    `yaml:"max_plate_slots"`
	Alphabet        string `yaml:"alphabet"`
	PadChar         string `yaml:"pad_char"`
	ImgHeight       int    `yaml:"img_height"`
	ImgWidth        int    `yaml:"img_width"`
	KeepAspectRatio bool   `yaml:"keep_aspect_ratio"`
	ImageColorMode  string `yaml:"image_color_mode"` // "rgb" or "grayscale"
}

func LoadPlateConfig(path string) (PlateConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return PlateConfig{}, err
	}
	var cfg PlateConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return PlateConfig{}, err
	}
	return cfg, nil
}

// ConfigFromEnv reads RELAY_LPR_* variables.
//
//	RELAY_LPR=1                         enable LPR
//	ORT_LIB_PATH=...                    onnxruntime shared library (optional)
//	RELAY_LPR_DETECTOR=path.onnx        plate detector ONNX
//	RELAY_LPR_OCR=path.onnx             plate OCR ONNX
//	RELAY_LPR_OCR_CONFIG=plate.yaml     OCR charset / input size
//	RELAY_LPR_DET_CONF=0.25             detector confidence threshold
//	RELAY_LPR_MIN_CONF=0.50             minimum mean char confidence to accept
func ConfigFromEnv() Config {
	enabled := os.Getenv("RELAY_LPR") == "1" || os.Getenv("RELAY_LPR") == "true"
	cfg := Config{
		Enabled:       enabled,
		ORTLib:        os.Getenv("ORT_LIB_PATH"),
		DetectorONNX:  envOr("RELAY_LPR_DETECTOR", ""),
		OCRONNX:       envOr("RELAY_LPR_OCR", ""),
		OCRConfigYAML: envOr("RELAY_LPR_OCR_CONFIG", ""),
		DetConf:       0.25,
		MinConf:       0.50,
	}
	if v := os.Getenv("RELAY_LPR_DET_CONF"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			cfg.DetConf = float32(f)
		}
	}
	if v := os.Getenv("RELAY_LPR_MIN_CONF"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.MinConf = f
		}
	}
	return cfg
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
