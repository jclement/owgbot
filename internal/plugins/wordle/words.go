package wordle

import (
	_ "embed"
	"strings"
	"sync"
)

// validWords is the guess dictionary (~19k five-letter English words,
// plurals included): the dwyl english-words list merged with Wordle's
// official allowed-guesses list. Answers are curated separately below;
// guesses only need to be real words.
//
//go:embed valid_words.txt
var validWordsRaw string

var (
	validOnce sync.Once
	validSet  map[string]bool
)

// isValidWord reports whether a guess is a real five-letter word.
func isValidWord(w string) bool {
	validOnce.Do(func() {
		words := strings.Fields(validWordsRaw)
		validSet = make(map[string]bool, len(words)+len(answers))
		for _, v := range words {
			validSet[v] = true
		}
		// The daily answers are always guessable, list or no list.
		for _, a := range answers {
			validSet[a] = true
		}
	})
	return validSet[w]
}

// answers is the daily-word pool: common five-letter words, no obscurities —
// this is played over LoRa, not against a dictionary snob.
var answers = []string{
	"about", "above", "abuse", "actor", "acute", "admit", "adopt", "adult", "after", "again",
	"agent", "agree", "ahead", "alarm", "album", "alert", "alike", "alive", "allow", "alone",
	"along", "alter", "among", "anger", "angle", "angry", "apart", "apple", "apply", "arena",
	"argue", "arise", "armor", "array", "arrow", "aside", "asset", "audio", "audit", "avoid",
	"award", "aware", "badge", "badly", "baker", "bases", "basic", "basis", "beach", "began",
	"begin", "being", "below", "bench", "billy", "birth", "black", "blame", "blank", "blast",
	"blind", "block", "blood", "board", "boost", "booth", "bound", "brain", "brand", "bread",
	"break", "breed", "brick", "brief", "bring", "broad", "broke", "brown", "build", "built",
	"bunch", "burst", "buyer", "cabin", "cable", "candy", "cargo", "carry", "catch", "cause",
	"chain", "chair", "chalk", "chaos", "charm", "chart", "chase", "cheap", "check", "chest",
	"chief", "child", "chill", "china", "chose", "civil", "claim", "class", "clean", "clear",
	"click", "climb", "clock", "close", "cloth", "cloud", "coach", "coast", "could", "count",
	"court", "cover", "craft", "crash", "crazy", "cream", "crime", "cross", "crowd", "crown",
	"crude", "curve", "cycle", "daily", "dance", "dated", "dealt", "death", "debut", "delay",
	"delta", "dense", "depth", "dirty", "doubt", "dozen", "draft", "drama", "drawn", "dream",
	"dress", "drift", "drill", "drink", "drive", "drove", "dying", "eager", "early", "earth",
	"eight", "elite", "empty", "enemy", "enjoy", "enter", "entry", "equal", "error", "event",
	"every", "exact", "exist", "extra", "faith", "false", "fault", "fiber", "field", "fifth",
	"fifty", "fight", "final", "first", "fixed", "flame", "flash", "fleet", "float", "floor",
	"fluid", "focus", "force", "forge", "forth", "forty", "forum", "found", "frame", "frank",
	"fraud", "fresh", "front", "frost", "fruit", "fully", "funny", "giant", "given", "glass",
	"globe", "glory", "goose", "grace", "grade", "grain", "grand", "grant", "grass", "grave",
	"great", "green", "greet", "group", "grown", "guard", "guess", "guest", "guide", "happy",
	"harsh", "haste", "heart", "heavy", "hedge", "hello", "hence", "hills", "hobby", "holds",
	"honey", "honor", "horse", "hotel", "house", "human", "humor", "ideal", "image", "index",
	"inner", "input", "issue", "japan", "joint", "jolly", "judge", "juice", "knife", "knock",
	"known", "label", "labor", "large", "laser", "later", "laugh", "layer", "learn", "lease",
	"least", "leave", "legal", "lemon", "level", "light", "limit", "lions", "lived", "liver",
	"local", "logic", "loose", "lower", "lucky", "lunch", "lying", "magic", "major", "maker",
	"march", "match", "maybe", "mayor", "meant", "medal", "media", "mercy", "metal", "meter",
	"midst", "might", "minor", "minus", "mixed", "model", "money", "month", "moral", "motor",
	"mount", "mouse", "mouth", "moved", "movie", "music", "needs", "nerve", "never", "newly",
	"night", "noble", "noise", "north", "noted", "novel", "nurse", "occur", "ocean", "offer",
	"often", "olive", "onion", "order", "other", "ought", "outer", "owner", "paint", "panel",
	"paper", "party", "patch", "pause", "peace", "phase", "phone", "photo", "piano", "piece",
	"pilot", "pitch", "place", "plain", "plane", "plant", "plate", "point", "porch", "pound",
	"power", "press", "price", "pride", "prime", "print", "prior", "prize", "proof", "proud",
	"prove", "pulse", "punch", "queen", "quick", "quiet", "quite", "radar", "radio", "raise",
	"rally", "ranch", "range", "rapid", "ratio", "reach", "ready", "realm", "rebel", "refer",
	"relax", "reply", "ridge", "rifle", "right", "rigid", "risky", "river", "robot", "rocky",
	"roman", "rough", "round", "route", "royal", "rural", "salad", "sauce", "scale", "scene",
	"scope", "score", "sense", "serve", "seven", "shade", "shake", "shall", "shame", "shape",
	"share", "sharp", "sheep", "sheet", "shelf", "shell", "shift", "shine", "shirt", "shock",
	"shoot", "shore", "short", "shown", "sight", "silly", "since", "sixth", "sixty", "sized",
	"skill", "slate", "sleep", "slice", "slide", "small", "smart", "smell", "smile", "smoke",
	"snake", "solar", "solid", "solve", "sorry", "sound", "south", "space", "spare", "spark",
	"speak", "speed", "spend", "spent", "spike", "spine", "split", "spoke", "sport", "staff",
	"stage", "stake", "stand", "start", "state", "steam", "steel", "steep", "steer", "stick",
	"still", "stock", "stone", "stood", "store", "storm", "story", "strip", "stuck", "study",
	"stuff", "style", "sugar", "suite", "sunny", "super", "surge", "sweet", "swift", "swing",
	"table", "taken", "taste", "teach", "teeth", "thank", "theme", "there", "these", "thick",
	"thing", "think", "third", "those", "three", "threw", "throw", "thumb", "tiger", "tight",
	"timer", "tired", "title", "toast", "today", "token", "topic", "total", "touch", "tough",
	"tower", "trace", "track", "trade", "trail", "train", "trait", "trash", "treat", "trend",
	"trial", "tribe", "trick", "tried", "tries", "truck", "truly", "trunk", "trust", "truth",
	"twice", "uncle", "under", "union", "unite", "unity", "until", "upper", "upset", "urban",
	"usage", "usual", "vague", "valid", "value", "video", "virus", "visit", "vital", "vivid",
	"vocal", "voice", "waste", "watch", "water", "wheel", "where", "which", "while", "white",
	"whole", "whose", "woman", "women", "world", "worry", "worse", "worst", "worth", "would",
	"wound", "write", "wrong", "wrote", "yield", "young", "youth",
}
