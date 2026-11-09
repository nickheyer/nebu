"""Trains the fixture tokenizers and writes reference encodings with the HuggingFace tokenizers package."""
import base64
import io
import json
import os
import sys

import sentencepiece as spm
from sentencepiece import sentencepiece_model_pb2 as spm_pb
from tokenizers import Tokenizer, AddedToken, decoders, models, normalizers, pre_tokenizers, processors, trainers

HERE = os.path.dirname(os.path.abspath(__file__))

CORPUS = [
    "The quick brown fox jumps over the lazy dog.",
    "Hello world! How are you doing today? I'm fine, thanks.",
    "Tokenization splits text into tokens; byte pair encoding merges frequent pairs.",
    "In 2024 the model served 1,234,567 requests at 99.9% uptime.",
    "Les enfants mangent des pommes dans le jardin de leur grand-mère.",
    "Der schnelle braune Fuchs springt über den faulen Hund.",
    "El rápido zorro marrón salta sobre el perro perezoso.",
    "今日はいい天気ですね。東京で寿司を食べました。",
    "中文分词是自然语言处理的基础任务之一。",
    "Привет, мир! Как дела? Всё хорошо.",
    "Καλημέρα κόσμε, τι κάνεις σήμερα;",
    "مرحبا بالعالم، كيف حالك اليوم؟",
    "def encode(text):\n    return [vocab[t] for t in split(text)]\n",
    "<s>system\nYou are a helpful assistant.</s><s>user\nWhat is the capital of France?</s>",
    "Emoji time 😀🎉🚀 and symbols © ® ™ ∑ ∫ √.",
    "Tabs\tand   multiple    spaces\n\nand newlines\r\nmixed.",
    "She said: \"don't\", he said: 'won't'. They're here; we've gone.",
    "Numbers 0123456789 and 3.14159 and -42 and 1e10.",
    "naïve café résumé coöperate Straße ÀÉÎÕÜ àéîõü",
    "ハローワールド、カタカナとひらがなのテスト。",
    "한국어 텍스트 토큰화 테스트입니다.",
    "Mixed 日本語 and English in one 文章 sentence.",
    "The user asked <tool_call>{\"name\": \"get_weather\"}</tool_call> for data.",
] * 4

PROBES = [
    "",
    "Hello world",
    " Hello world",
    "Hello world ",
    "  double  spaces  here  ",
    "The quick brown fox jumps over the lazy dog.",
    "I'm sure they're fine, don't worry; we've got it!",
    "Numbers 1234567890 and 3.14159 and 1,000,000.",
    "naïve café résumé Straße ÀÉÎÕÜ",
    "é å ö combining marks",
    "今日はいい天気ですね。東京で寿司を食べました。",
    "中文分词是自然语言处理的基础任务之一。",
    "Привет, мир! Как дела?",
    "مرحبا بالعالم",
    "한국어 텍스트 토큰화",
    "각 jamo sequence",
    "Emoji 😀🎉🚀 and ©®™∑",
    "tabs\tand\nnewlines\r\n\n\nmore",
    "def f(x):\n    return x * 2\n",
    "<s>system\nYou are helpful.</s>",
    "Tell me <s> about </s> special tokens inline",
    "[CLS] bert style [SEP] inline specials [MASK]",
    "the [MASK] is lstripped and rstripped [PAD] here",
    "UPPER CASE and MiXeD cAsE and İstanbul",
    " nbsp and em space　ideographic",
    "unassigned \U000e0080 and private  and control \x01 chars",
    "zero​width‌joiners‍here",
    "ends with newline\n",
    "\n",
    "   ",
    "a",
    "ab cd ef gh ij kl mn op qr st uv wx yz",
    "longwordwithoutanyspacesthatgoesonandonandonandonandon",
    "1234567890" * 5,
    "<|begin_of_text|>Hello<|end_of_text|>",
    "tool <tool_call>{\"name\": \"x\"}</tool_call> call",
    "Ｆｕｌｌｗｉｄｔｈ ｔｅｘｔ ①②③ ½ ﬁ ﬂ",
    "trailing   ",
    "xİy",
]

CL100K = r"(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+"
O200K = r"[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]*[\p{Ll}\p{Lm}\p{Lo}\p{M}]+(?i:'s|'t|'re|'ve|'m|'ll|'d)?|[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]+[\p{Ll}\p{Lm}\p{Lo}\p{M}]*(?i:'s|'t|'re|'ve|'m|'ll|'d)?|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n/]*|\s*[\r\n]+|\s+(?!\S)|\s+"


def byte_fallback_tokens():
    return ["<0x%02X>" % i for i in range(256)]


def llama_bpe():
    tok = Tokenizer(models.BPE(unk_token="<unk>", byte_fallback=True, fuse_unk=True))
    tok.normalizer = normalizers.Sequence([normalizers.Prepend("▁"), normalizers.Replace(" ", "▁")])
    tok.pre_tokenizer = None
    tok.decoder = decoders.Sequence([
        decoders.Replace("▁", " "),
        decoders.ByteFallback(),
        decoders.Fuse(),
        decoders.Strip(content=" ", left=1),
    ])
    trainer = trainers.BpeTrainer(vocab_size=1200, special_tokens=["<unk>", "<s>", "</s>"] + byte_fallback_tokens(), initial_alphabet=["▁"], show_progress=False)
    tok.train_from_iterator(CORPUS, trainer)
    tok.post_processor = processors.TemplateProcessing(
        single="<s> $A", pair="<s> $A <s> $B", special_tokens=[("<s>", tok.token_to_id("<s>"))])
    tok.add_special_tokens([AddedToken("<tool_call>", special=True), AddedToken("</tool_call>", special=True)])
    return tok


def llama3_bytelevel():
    tok = Tokenizer(models.BPE(ignore_merges=True))
    tok.normalizer = None
    tok.pre_tokenizer = pre_tokenizers.Sequence([
        pre_tokenizers.Split(pattern=__import__("tokenizers").Regex(CL100K), behavior="isolated", invert=False),
        pre_tokenizers.ByteLevel(add_prefix_space=False, trim_offsets=True, use_regex=False),
    ])
    tok.decoder = decoders.ByteLevel()
    tok.post_processor = None
    trainer = trainers.BpeTrainer(vocab_size=1500, special_tokens=[], initial_alphabet=pre_tokenizers.ByteLevel.alphabet(), show_progress=False)
    tok.train_from_iterator(CORPUS, trainer)
    tok.add_special_tokens([AddedToken("<|begin_of_text|>", special=True), AddedToken("<|end_of_text|>", special=True), AddedToken("<|eot_id|>", special=True)])
    tok.post_processor = processors.Sequence([
        processors.ByteLevel(trim_offsets=False),
        processors.TemplateProcessing(
            single="<|begin_of_text|> $A", pair="<|begin_of_text|> $A <|begin_of_text|> $B:1",
            special_tokens=[("<|begin_of_text|>", tok.token_to_id("<|begin_of_text|>"))]),
    ])
    return tok


def o200k_bytelevel():
    tok = Tokenizer(models.BPE(ignore_merges=True))
    tok.pre_tokenizer = pre_tokenizers.Sequence([
        pre_tokenizers.Split(pattern=__import__("tokenizers").Regex(O200K), behavior="isolated", invert=False),
        pre_tokenizers.ByteLevel(add_prefix_space=False, trim_offsets=True, use_regex=False),
    ])
    tok.decoder = decoders.ByteLevel()
    trainer = trainers.BpeTrainer(vocab_size=1300, special_tokens=[], initial_alphabet=pre_tokenizers.ByteLevel.alphabet(), show_progress=False)
    tok.train_from_iterator(CORPUS, trainer)
    tok.add_special_tokens([AddedToken("<|endoftext|>", special=True)])
    return tok


def gpt2_bytelevel():
    tok = Tokenizer(models.BPE())
    tok.pre_tokenizer = pre_tokenizers.ByteLevel(add_prefix_space=False, use_regex=True)
    tok.decoder = decoders.ByteLevel()
    tok.post_processor = processors.ByteLevel(trim_offsets=True)
    trainer = trainers.BpeTrainer(vocab_size=1300, special_tokens=["<|endoftext|>"], initial_alphabet=pre_tokenizers.ByteLevel.alphabet(), show_progress=False)
    tok.train_from_iterator(CORPUS, trainer)
    tok.add_tokens([AddedToken("tool_call", normalized=True)])
    return tok


def gpt2_prefix_space():
    tok = Tokenizer(models.BPE())
    tok.pre_tokenizer = pre_tokenizers.ByteLevel(add_prefix_space=True, use_regex=True)
    tok.decoder = decoders.ByteLevel()
    trainer = trainers.BpeTrainer(vocab_size=1200, special_tokens=["<s>", "</s>"], initial_alphabet=pre_tokenizers.ByteLevel.alphabet(), show_progress=False)
    tok.train_from_iterator(CORPUS, trainer)
    tok.post_processor = processors.RobertaProcessing(("</s>", tok.token_to_id("</s>")), ("<s>", tok.token_to_id("<s>")), trim_offsets=True, add_prefix_space=True)
    return tok


def bert_wordpiece():
    tok = Tokenizer(models.WordPiece(unk_token="[UNK]"))
    tok.normalizer = normalizers.BertNormalizer(clean_text=True, handle_chinese_chars=True, strip_accents=None, lowercase=True)
    tok.pre_tokenizer = pre_tokenizers.BertPreTokenizer()
    tok.decoder = decoders.WordPiece(prefix="##", cleanup=True)
    trainer = trainers.WordPieceTrainer(vocab_size=1500, special_tokens=["[PAD]", "[UNK]", "[CLS]", "[SEP]", "[MASK]"], show_progress=False)
    tok.train_from_iterator(CORPUS, trainer)
    tok.post_processor = processors.BertProcessing(("[SEP]", tok.token_to_id("[SEP]")), ("[CLS]", tok.token_to_id("[CLS]")))
    tok.add_special_tokens([AddedToken("[MASK]", lstrip=True, rstrip=True, special=True), AddedToken("[PAD]", lstrip=True, rstrip=True, special=True)])
    tok.add_tokens([AddedToken("bert", single_word=True, normalized=False), AddedToken("Hello", normalized=True)])
    return tok


def bert_cased_template():
    tok = Tokenizer(models.WordPiece(unk_token="[UNK]", max_input_chars_per_word=12))
    tok.normalizer = normalizers.Sequence([normalizers.NFD(), normalizers.StripAccents(), normalizers.Strip()])
    tok.pre_tokenizer = pre_tokenizers.Sequence([pre_tokenizers.WhitespaceSplit(), pre_tokenizers.Punctuation(behavior="isolated"), pre_tokenizers.Digits(individual_digits=True)])
    tok.decoder = decoders.WordPiece(prefix="##", cleanup=False)
    trainer = trainers.WordPieceTrainer(vocab_size=1500, special_tokens=["[PAD]", "[UNK]", "[CLS]", "[SEP]"], show_progress=False)
    tok.train_from_iterator(CORPUS, trainer)
    tok.post_processor = processors.TemplateProcessing(
        single="[CLS]:0 $A:0 [SEP]:0", pair="[CLS]:0 $A:0 [SEP]:0 $B:1 [SEP]:1",
        special_tokens=[("[CLS]", tok.token_to_id("[CLS]")), ("[SEP]", tok.token_to_id("[SEP]"))])
    return tok


def train_spm(prefix, model_type):
    path = os.path.join(HERE, "corpus.txt")
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(CORPUS))
    spm.SentencePieceTrainer.train(input=path, model_prefix=prefix, vocab_size=340, model_type=model_type, hard_vocab_limit=False,
                                   character_coverage=1.0, byte_fallback=False, unk_id=0, bos_id=1, eos_id=2, pad_id=-1,
                                   minloglevel=2)
    os.remove(path)
    proto = spm_pb.ModelProto()
    with open(prefix + ".model", "rb") as f:
        proto.ParseFromString(f.read())
    os.remove(prefix + ".model")
    os.remove(prefix + ".vocab")
    return proto


def unigram_from_spm(prefix):
    proto = train_spm(prefix, "unigram")
    vocab = [(p.piece, p.score) for p in proto.pieces]
    unk_id = 0
    tok = Tokenizer(models.Unigram(vocab, unk_id=unk_id, byte_fallback=False))
    charsmap = proto.normalizer_spec.precompiled_charsmap
    tok.normalizer = normalizers.Sequence([normalizers.Precompiled(charsmap), normalizers.Replace(__import__("tokenizers").Regex(" {2,}"), " ")])
    tok.pre_tokenizer = pre_tokenizers.Metaspace(replacement="▁", prepend_scheme="always", split=True)
    tok.decoder = decoders.Metaspace(replacement="▁", prepend_scheme="always", split=True)
    tok.post_processor = processors.TemplateProcessing(single="$A </s>", pair="$A </s> $B </s>", special_tokens=[("</s>", 2)])
    tok.add_special_tokens([AddedToken("<unk>", special=True), AddedToken("<s>", special=True), AddedToken("</s>", special=True)])
    return tok, charsmap


def unigram_whitespace_metaspace(base_json, charsmap):
    doc = json.loads(base_json)
    doc["pre_tokenizer"] = {"type": "Sequence", "pretokenizers": [
        {"type": "WhitespaceSplit"},
        {"type": "Metaspace", "replacement": "▁", "prepend_scheme": "always", "split": True},
    ]}
    doc["normalizer"] = {"type": "Sequence", "normalizers": [
        {"type": "Precompiled", "precompiled_charsmap": base64.b64encode(charsmap).decode("ascii")},
        {"type": "Nmt"},
        {"type": "NFKC"},
    ]}
    doc["decoder"] = {"type": "Metaspace", "replacement": "▁", "prepend_scheme": "first", "split": True}
    return Tokenizer.from_str(json.dumps(doc))


def unigram_first_scheme(base_json):
    doc = json.loads(base_json)
    doc["pre_tokenizer"] = {"type": "Metaspace", "replacement": "▁", "prepend_scheme": "first", "split": True}
    doc["normalizer"] = {"type": "Sequence", "normalizers": [{"type": "NFKC"}, {"type": "Lowercase"}, {"type": "Strip", "strip_left": True, "strip_right": True}]}
    doc["decoder"] = {"type": "Metaspace", "replacement": "▁", "prepend_scheme": "first", "split": True}
    doc["post_processor"] = None
    return Tokenizer.from_str(json.dumps(doc))


def spm_bpe(prefix):
    proto = train_spm(prefix, "bpe")
    vocab = {p.piece: i for i, p in enumerate(proto.pieces)}
    merges = []
    pieces = [p.piece for p in proto.pieces]
    scores = {p.piece: p.score for p in proto.pieces}
    for piece in sorted((p for p in pieces if scores[p] < 0), key=lambda p: -scores[p]):
        best = None
        for i in range(1, len(piece)):
            a, b = piece[:i], piece[i:]
            if a in vocab and b in vocab:
                cand = (scores[a] + scores[b], a, b)
                if best is None or cand[0] > best[0]:
                    best = cand
        if best is not None:
            merges.append((best[1], best[2]))
    tok = Tokenizer(models.BPE(vocab=vocab, merges=merges, unk_token="<unk>", fuse_unk=True, byte_fallback=False))
    tok.normalizer = normalizers.Sequence([normalizers.Nmt(), normalizers.NFKC(), normalizers.Replace(__import__("tokenizers").Regex(" {2,}"), " ")])
    tok.pre_tokenizer = pre_tokenizers.Metaspace(replacement="▁", prepend_scheme="always", split=False)
    tok.decoder = decoders.Metaspace(replacement="▁", prepend_scheme="always", split=False)
    tok.add_special_tokens([AddedToken("<unk>", special=True), AddedToken("<s>", special=True), AddedToken("</s>", special=True)])
    return tok


def wordlevel():
    tok = Tokenizer(models.WordLevel(unk_token="[UNK]"))
    tok.normalizer = normalizers.Sequence([normalizers.NFKC(), normalizers.Lowercase()])
    tok.pre_tokenizer = pre_tokenizers.Sequence([pre_tokenizers.Whitespace(), pre_tokenizers.Digits(individual_digits=False), pre_tokenizers.CharDelimiterSplit("-")])
    trainer = trainers.WordLevelTrainer(vocab_size=800, special_tokens=["[UNK]"], show_progress=False)
    tok.train_from_iterator(CORPUS, trainer)
    tok.decoder = decoders.BPEDecoder(suffix="</w>")
    return tok


def scripts_bpe():
    tok = Tokenizer(models.BPE(unk_token="<unk>", continuing_subword_prefix="##", end_of_word_suffix="</w>"))
    tok.pre_tokenizer = pre_tokenizers.Sequence([pre_tokenizers.UnicodeScripts(), pre_tokenizers.Split(pattern=__import__("tokenizers").Regex(r"\s+"), behavior="removed"), pre_tokenizers.Split(pattern="-", behavior="merged_with_previous")])
    tok.normalizer = normalizers.Lowercase()
    trainer = trainers.BpeTrainer(vocab_size=1200, special_tokens=["<unk>"], continuing_subword_prefix="##", end_of_word_suffix="</w>", show_progress=False)
    tok.train_from_iterator(CORPUS, trainer)
    tok.decoder = decoders.Sequence([decoders.BPEDecoder(suffix="</w>"), decoders.Replace("##", "")])
    return tok


def cases(tok):
    out = []
    for text in PROBES:
        enc = tok.encode(text, add_special_tokens=True)
        plain = tok.encode(text, add_special_tokens=False)
        out.append({
            "text": text,
            "ids": enc.ids,
            "tokens": enc.tokens,
            "ids_plain": plain.ids,
            "decoded": tok.decode(enc.ids, skip_special_tokens=True),
            "decoded_all": tok.decode(enc.ids, skip_special_tokens=False),
        })
    size = tok.get_vocab_size(with_added_tokens=True)
    singles = [{"id": i, "text": tok.decode([i], skip_special_tokens=False)} for i in range(size)]
    return out, singles


def main():
    fixtures = {}
    built = {
        "llama_bpe": llama_bpe(),
        "llama3_bytelevel": llama3_bytelevel(),
        "o200k_bytelevel": o200k_bytelevel(),
        "gpt2_bytelevel": gpt2_bytelevel(),
        "gpt2_prefix_space": gpt2_prefix_space(),
        "bert_wordpiece": bert_wordpiece(),
        "bert_cased_template": bert_cased_template(),
        "wordlevel": wordlevel(),
        "scripts_bpe": scripts_bpe(),
        "spm_bpe": spm_bpe(os.path.join(HERE, "spmbpe")),
    }
    uni, charsmap = unigram_from_spm(os.path.join(HERE, "spmuni"))
    built["unigram_spm"] = uni
    built["unigram_whitespace"] = unigram_whitespace_metaspace(uni.to_str(), charsmap)
    built["unigram_first"] = unigram_first_scheme(uni.to_str())
    for name, tok in built.items():
        path = os.path.join(HERE, name + ".json")
        tok.save(path)
        tok = Tokenizer.from_file(path)
        probes, singles = cases(tok)
        fixtures[name] = {"file": name + ".json", "cases": probes, "singles": singles}
    with open(os.path.join(HERE, "reference.json"), "w", encoding="utf-8") as f:
        json.dump({"tokenizers_version": __import__("tokenizers").__version__, "fixtures": fixtures}, f, ensure_ascii=False, indent=0)


if __name__ == "__main__":
    main()
