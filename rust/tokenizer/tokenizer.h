#ifndef TOKENIZER_H
#define TOKENIZER_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Load tokenizer from a JSON file. Returns 0 on success, -1 on error. */
int init_tokenizer(const char *path);

/* Count BPE tokens (without special tokens). Returns count or -1 on error. */
int count_tokens(const char *text);

/* Batch encode: fill out_lengths[i] with token count for texts[i].
   Returns 0 on success, -1 on error. */
int encode_batch(const char *const *texts, int count, uint32_t *out_lengths);

/* Free a string allocated by the library. */
void free_string(char *s);

#ifdef __cplusplus
}
#endif

#endif /* TOKENIZER_H */
