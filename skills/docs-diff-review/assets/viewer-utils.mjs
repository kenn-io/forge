export function normalizeLinkForSignature(value, prefix) {
  if (value === prefix) return "/";
  return value.startsWith(`${prefix}/`) ? value.slice(prefix.length) : value;
}

export function linkSignature(values, prefix) {
  return values.map((value) => normalizeLinkForSignature(value, prefix)).join("|");
}
