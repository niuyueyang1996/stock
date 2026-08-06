# Bundled Inno Setup language files

`ChineseSimplified.isl` is vendored from the official Inno Setup source repository
at verified commit `6ef32198ef1f7b7b375cd4b6b90896c2a58eb4c2`:

https://github.com/jrsoftware/issrc/blob/6ef32198ef1f7b7b375cd4b6b90896c2a58eb4c2/Files/Languages/ChineseSimplified.isl

SHA-256: `e0b0b350e2245f3c5e65586dfe43d574f6e7f06f2261149aba284954b3fc9a8d`

It is bundled with the installer source so CI does not depend on optional language
files being present in the Chocolatey installation of Inno Setup.

The file declares compatibility with Inno Setup 6.5.0+. Its 296 section/message
keys were also compared against the official Inno Setup 6.7.1 `Default.isl` and
match exactly (no missing or extra keys).
