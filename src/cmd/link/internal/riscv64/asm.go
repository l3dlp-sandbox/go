// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package riscv64

import (
	"cmd/internal/obj/riscv"
	"cmd/internal/objabi"
	"cmd/internal/sys"
	"cmd/link/internal/ld"
	"cmd/link/internal/loader"
	"cmd/link/internal/sym"
	"debug/elf"
	"fmt"
	"log"
	"sort"
)

// fakeLabelName matches the RISCV_FAKE_LABEL_NAME from binutils.
const fakeLabelName = ".L0 "

func gentext(ctxt *ld.Link, ldr *loader.Loader) {
	initfunc, addmoduledata := ld.PrepareAddmoduledata(ctxt)
	if initfunc == nil {
		return
	}

	// Emit the following function:
	//
	// go.link.addmoduledatainit:
	//      auipc a0, %pcrel_hi(local.moduledata)
	//      addi  a0, %pcrel_lo(local.moduledata)
	//      j     runtime.addmoduledata

	sz := initfunc.AddSymRef(ctxt.Arch, ctxt.Moduledata, 0, objabi.R_RISCV_PCREL_ITYPE, 8)
	initfunc.SetUint32(ctxt.Arch, sz-8, 0x00000517) // auipc a0, %pcrel_hi(local.moduledata)
	initfunc.SetUint32(ctxt.Arch, sz-4, 0x00050513) // addi  a0, %pcrel_lo(local.moduledata)

	sz = initfunc.AddSymRef(ctxt.Arch, addmoduledata, 0, objabi.R_RISCV_JAL, 4)
	initfunc.SetUint32(ctxt.Arch, sz-4, 0x0000006f) // j runtime.addmoduledata
}

func findHI20Reloc(ldr *loader.Loader, s loader.Sym, val int64) *loader.Reloc {
	outer := ldr.OuterSym(s)
	if outer == 0 {
		return nil
	}
	relocs := ldr.Relocs(outer)
	start := sort.Search(relocs.Count(), func(i int) bool { return ldr.SymValue(outer)+int64(relocs.At(i).Off()) >= val })
	for idx := start; idx < relocs.Count(); idx++ {
		r := relocs.At(idx)
		if ldr.SymValue(outer)+int64(r.Off()) != val {
			break
		}
		if r.Type() == objabi.R_RISCV_GOT_HI20 || r.Type() == objabi.R_RISCV_PCREL_HI20 {
			return &r
		}
	}
	return nil
}

func adddynrel(target *ld.Target, ldr *loader.Loader, syms *ld.ArchSyms, s loader.Sym, r loader.Reloc, rIdx int) bool {
	targ := r.Sym()

	var targType sym.SymKind
	if targ != 0 {
		targType = ldr.SymType(targ)
	}

	switch r.Type() {
	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_RISCV_CALL),
		objabi.ElfRelocOffset + objabi.RelocType(elf.R_RISCV_CALL_PLT):

		if targType == sym.SDYNIMPORT {
			addpltsym(target, ldr, syms, targ)
			su := ldr.MakeSymbolUpdater(s)
			su.SetRelocSym(rIdx, syms.PLT)
			su.SetRelocAdd(rIdx, r.Add()+int64(ldr.SymPlt(targ)))
		}
		if targType == 0 || targType == sym.SXREF {
			ldr.Errorf(s, "unknown symbol %s in RISCV call", ldr.SymName(targ))
		}
		su := ldr.MakeSymbolUpdater(s)
		su.SetRelocType(rIdx, objabi.R_RISCV_CALL)
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_RISCV_GOT_HI20):
		if targType != sym.SDYNIMPORT {
			// TODO(jsing): Could convert to non-GOT reference.
		}

		ld.AddGotSym(target, ldr, syms, targ, uint32(elf.R_RISCV_64))
		su := ldr.MakeSymbolUpdater(s)
		su.SetRelocType(rIdx, objabi.R_RISCV_GOT_HI20)
		su.SetRelocSym(rIdx, syms.GOT)
		su.SetRelocAdd(rIdx, r.Add()+int64(ldr.SymGot(targ)))
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_RISCV_PCREL_HI20):
		su := ldr.MakeSymbolUpdater(s)
		su.SetRelocType(rIdx, objabi.R_RISCV_PCREL_HI20)
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_RISCV_PCREL_LO12_I):
		if r.Add() != 0 {
			ldr.Errorf(s, "R_RISCV_PCREL_LO12_I with non-zero addend")
		}
		su := ldr.MakeSymbolUpdater(s)
		su.SetRelocType(rIdx, objabi.R_RISCV_PCREL_LO12_I)
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_RISCV_PCREL_LO12_S):
		if r.Add() != 0 {
			ldr.Errorf(s, "R_RISCV_PCREL_LO12_S with non-zero addend")
		}
		su := ldr.MakeSymbolUpdater(s)
		su.SetRelocType(rIdx, objabi.R_RISCV_PCREL_LO12_S)
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_RISCV_RVC_BRANCH):
		su := ldr.MakeSymbolUpdater(s)
		su.SetRelocType(rIdx, objabi.R_RISCV_RVC_BRANCH)
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_RISCV_RVC_JUMP):
		su := ldr.MakeSymbolUpdater(s)
		su.SetRelocType(rIdx, objabi.R_RISCV_RVC_JUMP)
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_RISCV_BRANCH):
		su := ldr.MakeSymbolUpdater(s)
		su.SetRelocType(rIdx, objabi.R_RISCV_BRANCH)
		return true

	case objabi.ElfRelocOffset + objabi.RelocType(elf.R_RISCV_RELAX):
		// Ignore relaxations, at least for now.
		return true

	default:
		if r.Type() >= objabi.ElfRelocOffset {
			ldr.Errorf(s, "unexpected relocation type %d (%s)", r.Type(), sym.RelocName(target.Arch, r.Type()))
			return false
		}
	}

	// Reread the reloc to incorporate any changes in type above.
	relocs := ldr.Relocs(s)
	r = relocs.At(rIdx)

	switch r.Type() {
	case objabi.R_RISCV_CALL:
		if targType != sym.SDYNIMPORT {
			// nothing to do, the relocation will be laid out in reloc
			return true
		}
		if target.IsExternal() {
			// External linker will do this relocation.
			return true
		}
		// Internal linking.
		if r.Add() != 0 {
			ldr.Errorf(s, "PLT reference with non-zero addend (%v)", r.Add())
		}
		// Build a PLT entry and change the relocation target to that entry.
		addpltsym(target, ldr, syms, targ)
		su := ldr.MakeSymbolUpdater(s)
		su.SetRelocSym(rIdx, syms.PLT)
		su.SetRelocAdd(rIdx, int64(ldr.SymPlt(targ)))

		return true
	}

	return false
}

func genSymsLate(ctxt *ld.Link, ldr *loader.Loader) {
	if ctxt.LinkMode != ld.LinkExternal {
		return
	}

	// Generate a local text symbol for each relocation target, as the
	// R_RISCV_PCREL_LO12_* relocations generated by elfreloc1 need it.
	if ctxt.Textp == nil {
		log.Fatal("genSymsLate called before Textp has been assigned")
	}
	var hi20Syms []loader.Sym
	for _, s := range ctxt.Textp {
		relocs := ldr.Relocs(s)
		for ri := 0; ri < relocs.Count(); ri++ {
			r := relocs.At(ri)
			if r.Type() != objabi.R_RISCV_CALL &&
				r.Type() != objabi.R_RISCV_PCREL_ITYPE &&
				r.Type() != objabi.R_RISCV_PCREL_STYPE &&
				r.Type() != objabi.R_RISCV_TLS_IE &&
				r.Type() != objabi.R_RISCV_GOT_PCREL_ITYPE {
				continue
			}
			if r.Off() == 0 && ldr.SymType(s).IsText() {
				// Use the symbol for the function instead of creating
				// an overlapping symbol.
				continue
			}

			// TODO(jsing): Consider generating ELF symbols without needing
			// loader symbols, in order to reduce memory consumption. This
			// would require changes to genelfsym so that it called
			// putelfsym and putelfsyment as appropriate.
			sb := ldr.MakeSymbolBuilder(fakeLabelName)
			sb.SetType(sym.STEXT)
			sb.SetValue(ldr.SymValue(s) + int64(r.Off()))
			sb.SetLocal(true)
			sb.SetReachable(true)
			sb.SetVisibilityHidden(true)
			sb.SetSect(ldr.SymSect(s))
			if outer := ldr.OuterSym(s); outer != 0 {
				ldr.AddInteriorSym(outer, sb.Sym())
			}
			hi20Syms = append(hi20Syms, sb.Sym())
		}
	}
	ctxt.Textp = append(ctxt.Textp, hi20Syms...)
	ldr.SortSyms(ctxt.Textp)
}

func findHI20Symbol(ctxt *ld.Link, ldr *loader.Loader, val int64) loader.Sym {
	idx := sort.Search(len(ctxt.Textp), func(i int) bool { return ldr.SymValue(ctxt.Textp[i]) >= val })
	if idx >= len(ctxt.Textp) {
		return 0
	}
	if s := ctxt.Textp[idx]; ldr.SymValue(s) == val && ldr.SymType(s).IsText() {
		return s
	}
	return 0
}

func elfreloc1(ctxt *ld.Link, out *ld.OutBuf, ldr *loader.Loader, s loader.Sym, r loader.ExtReloc, ri int, sectoff int64) bool {
	elfsym := ld.ElfSymForReloc(ctxt, r.Xsym)
	switch r.Type {
	case objabi.R_ADDR, objabi.R_DWARFSECREF:
		out.Write64(uint64(sectoff))
		switch r.Size {
		case 4:
			out.Write64(uint64(elf.R_RISCV_32) | uint64(elfsym)<<32)
		case 8:
			out.Write64(uint64(elf.R_RISCV_64) | uint64(elfsym)<<32)
		default:
			ld.Errorf("unknown size %d for %v relocation", r.Size, r.Type)
			return false
		}
		out.Write64(uint64(r.Xadd))

	case objabi.R_RISCV_JAL, objabi.R_RISCV_JAL_TRAMP:
		out.Write64(uint64(sectoff))
		out.Write64(uint64(elf.R_RISCV_JAL) | uint64(elfsym)<<32)
		out.Write64(uint64(r.Xadd))

	case objabi.R_RISCV_CALL,
		objabi.R_RISCV_PCREL_ITYPE,
		objabi.R_RISCV_PCREL_STYPE,
		objabi.R_RISCV_TLS_IE,
		objabi.R_RISCV_GOT_PCREL_ITYPE:
		// Find the text symbol for the AUIPC instruction targeted
		// by this relocation.
		relocs := ldr.Relocs(s)
		offset := int64(relocs.At(ri).Off())
		hi20Sym := findHI20Symbol(ctxt, ldr, ldr.SymValue(s)+offset)
		if hi20Sym == 0 {
			ld.Errorf("failed to find text symbol for HI20 relocation at %d (%x)", sectoff, ldr.SymValue(s)+offset)
			return false
		}
		hi20ElfSym := ld.ElfSymForReloc(ctxt, hi20Sym)

		// Emit two relocations - a R_RISCV_PCREL_HI20 relocation and a
		// corresponding R_RISCV_PCREL_LO12_I or R_RISCV_PCREL_LO12_S relocation.
		// Note that the LO12 relocation must point to a target that has a valid
		// HI20 PC-relative relocation text symbol, which in turn points to the
		// given symbol. For further details see section 8.4.9 of the RISC-V ABIs
		// Specification:
		//
		//  https://github.com/riscv-non-isa/riscv-elf-psabi-doc/releases/download/v1.0/riscv-abi.pdf
		//
		var hiRel, loRel elf.R_RISCV
		switch r.Type {
		case objabi.R_RISCV_CALL, objabi.R_RISCV_PCREL_ITYPE:
			hiRel, loRel = elf.R_RISCV_PCREL_HI20, elf.R_RISCV_PCREL_LO12_I
		case objabi.R_RISCV_PCREL_STYPE:
			hiRel, loRel = elf.R_RISCV_PCREL_HI20, elf.R_RISCV_PCREL_LO12_S
		case objabi.R_RISCV_TLS_IE:
			hiRel, loRel = elf.R_RISCV_TLS_GOT_HI20, elf.R_RISCV_PCREL_LO12_I
		case objabi.R_RISCV_GOT_PCREL_ITYPE:
			hiRel, loRel = elf.R_RISCV_GOT_HI20, elf.R_RISCV_PCREL_LO12_I
		}
		out.Write64(uint64(sectoff))
		out.Write64(uint64(hiRel) | uint64(elfsym)<<32)
		out.Write64(uint64(r.Xadd))
		out.Write64(uint64(sectoff + 4))
		out.Write64(uint64(loRel) | uint64(hi20ElfSym)<<32)
		out.Write64(uint64(0))

	case objabi.R_RISCV_TLS_LE:
		out.Write64(uint64(sectoff))
		out.Write64(uint64(elf.R_RISCV_TPREL_HI20) | uint64(elfsym)<<32)
		out.Write64(uint64(r.Xadd))
		out.Write64(uint64(sectoff + 4))
		out.Write64(uint64(elf.R_RISCV_TPREL_LO12_I) | uint64(elfsym)<<32)
		out.Write64(uint64(r.Xadd))

	default:
		return false
	}

	return true
}

func elfsetupplt(ctxt *ld.Link, ldr *loader.Loader, plt, gotplt *loader.SymbolBuilder, dynamic loader.Sym) {
	if plt.Size() != 0 {
		return
	}
	if gotplt.Size() != 0 {
		ctxt.Errorf(gotplt.Sym(), "got.plt is not empty")
	}

	// See section 8.4.6 of the RISC-V ABIs Specification:
	//
	//  https://github.com/riscv-non-isa/riscv-elf-psabi-doc/releases/download/v1.0/riscv-abi.pdf
	//
	// 1:   auipc  t2, %pcrel_hi(.got.plt)
	//      sub    t1, t1, t3               # shifted .got.plt offset + hdr size + 12
	//      l[w|d] t3, %pcrel_lo(1b)(t2)    # _dl_runtime_resolve
	//      addi   t1, t1, -(hdr size + 12) # shifted .got.plt offset
	//      addi   t0, t2, %pcrel_lo(1b)    # &.got.plt
	//      srli   t1, t1, log2(16/PTRSIZE) # .got.plt offset
	//      l[w|d] t0, PTRSIZE(t0)          # link map
	//      jr     t3

	plt.AddSymRef(ctxt.Arch, gotplt.Sym(), 0, objabi.R_RISCV_PCREL_HI20, 4)
	plt.SetUint32(ctxt.Arch, plt.Size()-4, 0x00000397) // auipc   t2,0x0

	sb := ldr.MakeSymbolBuilder(fakeLabelName)
	sb.SetType(sym.STEXT)
	sb.SetValue(ldr.SymValue(plt.Sym()) + plt.Size() - 4)
	sb.SetLocal(true)
	sb.SetReachable(true)
	sb.SetVisibilityHidden(true)
	plt.AddInteriorSym(sb.Sym())

	plt.AddUint32(ctxt.Arch, 0x41c30333) // sub     t1,t1,t3

	plt.AddSymRef(ctxt.Arch, sb.Sym(), 0, objabi.R_RISCV_PCREL_LO12_I, 4)
	plt.SetUint32(ctxt.Arch, plt.Size()-4, 0x0003be03) // ld      t3,0(t2)

	plt.AddUint32(ctxt.Arch, 0xfd430313) // addi    t1,t1,-44

	plt.AddSymRef(ctxt.Arch, sb.Sym(), 0, objabi.R_RISCV_PCREL_LO12_I, 4)
	plt.SetUint32(ctxt.Arch, plt.Size()-4, 0x00038293) // addi    t0,t2,0

	plt.AddUint32(ctxt.Arch, 0x00135313) // srli    t1,t1,0x1
	plt.AddUint32(ctxt.Arch, 0x0082b283) // ld      t0,8(t0)
	plt.AddUint32(ctxt.Arch, 0x00008e02) // jr      t3

	gotplt.AddAddrPlus(ctxt.Arch, dynamic, 0) // got.plt[0] = _dl_runtime_resolve
	gotplt.AddUint64(ctxt.Arch, 0)            // got.plt[1] = link map
}

func addpltsym(target *ld.Target, ldr *loader.Loader, syms *ld.ArchSyms, s loader.Sym) {
	if ldr.SymPlt(s) >= 0 {
		return
	}

	ld.Adddynsym(ldr, target, syms, s)

	plt := ldr.MakeSymbolUpdater(syms.PLT)
	gotplt := ldr.MakeSymbolUpdater(syms.GOTPLT)
	rela := ldr.MakeSymbolUpdater(syms.RelaPLT)
	if plt.Size() == 0 {
		panic("plt is not set up")
	}

	// See section 8.4.6 of the RISC-V ABIs Specification:
	//
	//  https://github.com/riscv-non-isa/riscv-elf-psabi-doc/releases/download/v1.0/riscv-abi.pdf
	//
	// 1:  auipc   t3, %pcrel_hi(function@.got.plt)
	//     l[w|d]  t3, %pcrel_lo(1b)(t3)
	//     jalr    t1, t3
	//     nop

	plt.AddSymRef(target.Arch, gotplt.Sym(), gotplt.Size(), objabi.R_RISCV_PCREL_HI20, 4)
	plt.SetUint32(target.Arch, plt.Size()-4, 0x00000e17) // auipc   t3,0x0

	sb := ldr.MakeSymbolBuilder(fakeLabelName)
	sb.SetType(sym.STEXT)
	sb.SetValue(ldr.SymValue(plt.Sym()) + plt.Size() - 4)
	sb.SetLocal(true)
	sb.SetReachable(true)
	sb.SetVisibilityHidden(true)
	plt.AddInteriorSym(sb.Sym())

	plt.AddSymRef(target.Arch, sb.Sym(), 0, objabi.R_RISCV_PCREL_LO12_I, 4)
	plt.SetUint32(target.Arch, plt.Size()-4, 0x000e3e03) // ld      t3,0(t3)
	plt.AddUint32(target.Arch, 0x000e0367)               // jalr    t1,t3
	plt.AddUint32(target.Arch, 0x00000001)               // nop

	ldr.SetPlt(s, int32(plt.Size()-16))

	// add to got.plt: pointer to plt[0]
	gotplt.AddAddrPlus(target.Arch, plt.Sym(), 0)

	// rela
	rela.AddAddrPlus(target.Arch, gotplt.Sym(), gotplt.Size()-8)
	sDynid := ldr.SymDynid(s)

	rela.AddUint64(target.Arch, elf.R_INFO(uint32(sDynid), uint32(elf.R_RISCV_JUMP_SLOT)))
	rela.AddUint64(target.Arch, 0)
}

func machoreloc1(*sys.Arch, *ld.OutBuf, *loader.Loader, loader.Sym, loader.ExtReloc, int64) bool {
	log.Fatalf("machoreloc1 not implemented")
	return false
}

func archreloc(target *ld.Target, ldr *loader.Loader, syms *ld.ArchSyms, r loader.Reloc, s loader.Sym, val int64) (o int64, nExtReloc int, ok bool) {
	rs := r.Sym()
	pc := ldr.SymValue(s) + int64(r.Off())

	// If the call points to a trampoline, see if we can reach the symbol
	// directly. This situation can occur when the relocation symbol is
	// not assigned an address until after the trampolines are generated.
	if r.Type() == objabi.R_RISCV_JAL_TRAMP {
		relocs := ldr.Relocs(rs)
		if relocs.Count() != 1 {
			ldr.Errorf(s, "trampoline %v has %d relocations", ldr.SymName(rs), relocs.Count())
		}
		tr := relocs.At(0)
		if tr.Type() != objabi.R_RISCV_CALL {
			ldr.Errorf(s, "trampoline %v has unexpected relocation %v", ldr.SymName(rs), tr.Type())
		}
		trs := tr.Sym()
		if ldr.SymValue(trs) != 0 && ldr.SymType(trs) != sym.SDYNIMPORT && ldr.SymType(trs) != sym.SUNDEFEXT {
			trsOff := ldr.SymValue(trs) + tr.Add() - pc
			if trsOff >= -(1<<20) && trsOff < (1<<20) {
				r.SetType(objabi.R_RISCV_JAL)
				r.SetSym(trs)
				r.SetAdd(tr.Add())
				rs = trs
			}
		}

	}

	if target.IsExternal() {
		switch r.Type() {
		case objabi.R_RISCV_JAL, objabi.R_RISCV_JAL_TRAMP:
			return val, 1, true

		case objabi.R_RISCV_CALL, objabi.R_RISCV_PCREL_ITYPE, objabi.R_RISCV_PCREL_STYPE, objabi.R_RISCV_TLS_IE, objabi.R_RISCV_TLS_LE, objabi.R_RISCV_GOT_PCREL_ITYPE:
			return val, 2, true
		}

		return val, 0, false
	}

	off := ldr.SymValue(rs) + r.Add() - pc

	switch r.Type() {
	case objabi.R_RISCV_JAL, objabi.R_RISCV_JAL_TRAMP:
		// Generate instruction immediates.
		imm, err := riscv.EncodeJImmediate(off)
		if err != nil {
			ldr.Errorf(s, "cannot encode J-type instruction relocation offset for %s: %v", ldr.SymName(rs), err)
		}
		immMask := int64(riscv.JTypeImmMask)

		val = (val &^ immMask) | int64(imm)

		return val, 0, true

	case objabi.R_RISCV_TLS_IE:
		log.Fatalf("cannot handle R_RISCV_TLS_IE (sym %s) when linking internally", ldr.SymName(s))
		return val, 0, false

	case objabi.R_RISCV_TLS_LE:
		// Generate LUI and ADDIW instruction immediates.
		off := r.Add()

		low, high, err := riscv.Split32BitImmediate(off)
		if err != nil {
			ldr.Errorf(s, "relocation does not fit in 32-bits: %d", off)
		}

		luiImm, err := riscv.EncodeUImmediate(high)
		if err != nil {
			ldr.Errorf(s, "cannot encode R_RISCV_TLS_LE LUI relocation offset for %s: %v", ldr.SymName(rs), err)
		}

		addiwImm, err := riscv.EncodeIImmediate(low)
		if err != nil {
			ldr.Errorf(s, "cannot encode R_RISCV_TLS_LE I-type instruction relocation offset for %s: %v", ldr.SymName(rs), err)
		}

		lui := int64(uint32(val))
		addiw := int64(uint32(val >> 32))

		lui = (lui &^ riscv.UTypeImmMask) | int64(uint32(luiImm))
		addiw = (addiw &^ riscv.ITypeImmMask) | int64(uint32(addiwImm))

		return addiw<<32 | lui, 0, true

	case objabi.R_RISCV_BRANCH:
		pc := ldr.SymValue(s) + int64(r.Off())
		off := ldr.SymValue(rs) + r.Add() - pc

		imm, err := riscv.EncodeBImmediate(off)
		if err != nil {
			ldr.Errorf(s, "cannot encode B-type instruction relocation offset for %s: %v", ldr.SymName(rs), err)
		}
		ins := (int64(uint32(val)) &^ riscv.BTypeImmMask) | int64(uint32(imm))

		return ins, 0, true

	case objabi.R_RISCV_RVC_BRANCH, objabi.R_RISCV_RVC_JUMP:
		pc := ldr.SymValue(s) + int64(r.Off())
		off := ldr.SymValue(rs) + r.Add() - pc

		var err error
		var imm, immMask int64
		switch r.Type() {
		case objabi.R_RISCV_RVC_BRANCH:
			immMask = riscv.CBTypeImmMask
			imm, err = riscv.EncodeCBImmediate(off)
			if err != nil {
				ldr.Errorf(s, "cannot encode CB-type instruction relocation offset for %s: %v", ldr.SymName(rs), err)
			}
		case objabi.R_RISCV_RVC_JUMP:
			immMask = riscv.CJTypeImmMask
			imm, err = riscv.EncodeCJImmediate(off)
			if err != nil {
				ldr.Errorf(s, "cannot encode CJ-type instruction relocation offset for %s: %v", ldr.SymName(rs), err)
			}
		default:
			panic(fmt.Sprintf("unknown relocation type: %v", r.Type()))
		}

		ins := (int64(uint16(val)) &^ immMask) | int64(uint16(imm))

		return ins, 0, true

	case objabi.R_RISCV_GOT_HI20, objabi.R_RISCV_PCREL_HI20:
		pc := ldr.SymValue(s) + int64(r.Off())
		off := ldr.SymValue(rs) + r.Add() - pc

		// Generate AUIPC immediates.
		_, high, err := riscv.Split32BitImmediate(off)
		if err != nil {
			ldr.Errorf(s, "relocation does not fit in 32-bits: %d", off)
		}

		auipcImm, err := riscv.EncodeUImmediate(high)
		if err != nil {
			ldr.Errorf(s, "cannot encode R_RISCV_PCREL_ AUIPC relocation offset for %s: %v", ldr.SymName(rs), err)
		}

		auipc := int64(uint32(val))
		auipc = (auipc &^ riscv.UTypeImmMask) | int64(uint32(auipcImm))

		return auipc, 0, true

	case objabi.R_RISCV_PCREL_LO12_I, objabi.R_RISCV_PCREL_LO12_S:
		hi20Reloc := findHI20Reloc(ldr, rs, ldr.SymValue(rs))
		if hi20Reloc == nil {
			ldr.Errorf(s, "missing HI20 relocation for LO12 relocation with %s (%d)", ldr.SymName(rs), rs)
		}

		pc := ldr.SymValue(s) + int64(hi20Reloc.Off())
		off := ldr.SymValue(hi20Reloc.Sym()) + hi20Reloc.Add() - pc

		low, _, err := riscv.Split32BitImmediate(off)
		if err != nil {
			ldr.Errorf(s, "relocation does not fit in 32-bits: %d", off)
		}

		var imm, immMask int64
		switch r.Type() {
		case objabi.R_RISCV_PCREL_LO12_I:
			immMask = riscv.ITypeImmMask
			imm, err = riscv.EncodeIImmediate(low)
			if err != nil {
				ldr.Errorf(s, "cannot encode objabi.R_RISCV_PCREL_LO12_I I-type instruction relocation offset for %s: %v", ldr.SymName(rs), err)
			}
		case objabi.R_RISCV_PCREL_LO12_S:
			immMask = riscv.STypeImmMask
			imm, err = riscv.EncodeSImmediate(low)
			if err != nil {
				ldr.Errorf(s, "cannot encode R_RISCV_PCREL_LO12_S S-type instruction relocation offset for %s: %v", ldr.SymName(rs), err)
			}
		default:
			panic(fmt.Sprintf("unknown relocation type: %v", r.Type()))
		}

		ins := int64(uint32(val))
		ins = (ins &^ immMask) | int64(uint32(imm))
		return ins, 0, true

	case objabi.R_RISCV_CALL, objabi.R_RISCV_PCREL_ITYPE, objabi.R_RISCV_PCREL_STYPE:
		// Generate AUIPC and second instruction immediates.
		low, high, err := riscv.Split32BitImmediate(off)
		if err != nil {
			ldr.Errorf(s, "pc-relative relocation does not fit in 32 bits: %d", off)
		}

		auipcImm, err := riscv.EncodeUImmediate(high)
		if err != nil {
			ldr.Errorf(s, "cannot encode AUIPC relocation offset for %s: %v", ldr.SymName(rs), err)
		}

		var secondImm, secondImmMask int64
		switch r.Type() {
		case objabi.R_RISCV_CALL, objabi.R_RISCV_PCREL_ITYPE:
			secondImmMask = riscv.ITypeImmMask
			secondImm, err = riscv.EncodeIImmediate(low)
			if err != nil {
				ldr.Errorf(s, "cannot encode I-type instruction relocation offset for %s: %v", ldr.SymName(rs), err)
			}
		case objabi.R_RISCV_PCREL_STYPE:
			secondImmMask = riscv.STypeImmMask
			secondImm, err = riscv.EncodeSImmediate(low)
			if err != nil {
				ldr.Errorf(s, "cannot encode S-type instruction relocation offset for %s: %v", ldr.SymName(rs), err)
			}
		default:
			panic(fmt.Sprintf("unknown relocation type: %v", r.Type()))
		}

		auipc := int64(uint32(val))
		second := int64(uint32(val >> 32))

		auipc = (auipc &^ riscv.UTypeImmMask) | int64(uint32(auipcImm))
		second = (second &^ secondImmMask) | int64(uint32(secondImm))

		return second<<32 | auipc, 0, true
	}

	return val, 0, false
}

func archrelocvariant(*ld.Target, *loader.Loader, loader.Reloc, sym.RelocVariant, loader.Sym, int64, []byte) int64 {
	log.Fatalf("archrelocvariant")
	return -1
}

func extreloc(target *ld.Target, ldr *loader.Loader, r loader.Reloc, s loader.Sym) (loader.ExtReloc, bool) {
	switch r.Type() {
	case objabi.R_RISCV_JAL, objabi.R_RISCV_JAL_TRAMP:
		return ld.ExtrelocSimple(ldr, r), true

	case objabi.R_RISCV_CALL, objabi.R_RISCV_PCREL_ITYPE, objabi.R_RISCV_PCREL_STYPE, objabi.R_RISCV_TLS_IE, objabi.R_RISCV_TLS_LE, objabi.R_RISCV_GOT_PCREL_ITYPE:
		return ld.ExtrelocViaOuterSym(ldr, r, s), true
	}
	return loader.ExtReloc{}, false
}

func trampoline(ctxt *ld.Link, ldr *loader.Loader, ri int, rs, s loader.Sym) {
	relocs := ldr.Relocs(s)
	r := relocs.At(ri)

	switch r.Type() {
	case objabi.R_RISCV_JAL:
		pc := ldr.SymValue(s) + int64(r.Off())
		off := ldr.SymValue(rs) + r.Add() - pc

		// Relocation symbol has an address and is directly reachable,
		// therefore there is no need for a trampoline.
		if ldr.SymValue(rs) != 0 && off >= -(1<<20) && off < (1<<20) && (*ld.FlagDebugTramp <= 1 || ldr.SymPkg(s) == ldr.SymPkg(rs)) {
			break
		}

		// Relocation symbol is too far for a direct call or has not
		// yet been given an address. See if an existing trampoline is
		// reachable and if so, reuse it. Otherwise we need to create
		// a new trampoline.
		var tramp loader.Sym
		for i := 0; ; i++ {
			oName := ldr.SymName(rs)
			name := fmt.Sprintf("%s-tramp%d", oName, i)
			if r.Add() != 0 {
				name = fmt.Sprintf("%s%+x-tramp%d", oName, r.Add(), i)
			}
			tramp = ldr.LookupOrCreateSym(name, int(ldr.SymVersion(rs)))
			ldr.SetAttrReachable(tramp, true)
			if ldr.SymType(tramp) == sym.SDYNIMPORT {
				// Do not reuse trampoline defined in other module.
				continue
			}
			if oName == "runtime.deferreturn" {
				ldr.SetIsDeferReturnTramp(tramp, true)
			}
			if ldr.SymValue(tramp) == 0 {
				// Either trampoline does not exist or we found one
				// that does not have an address assigned and will be
				// laid down immediately after the current function.
				break
			}

			trampOff := ldr.SymValue(tramp) - (ldr.SymValue(s) + int64(r.Off()))
			if trampOff >= -(1<<20) && trampOff < (1<<20) {
				// An existing trampoline that is reachable.
				break
			}
		}
		if ldr.SymType(tramp) == 0 {
			trampb := ldr.MakeSymbolUpdater(tramp)
			ctxt.AddTramp(trampb, ldr.SymType(s))
			genCallTramp(ctxt.Arch, ctxt.LinkMode, ldr, trampb, rs, int64(r.Add()))
		}
		sb := ldr.MakeSymbolUpdater(s)
		if ldr.SymValue(rs) == 0 {
			// In this case the target symbol has not yet been assigned an
			// address, so we have to assume a trampoline is required. Mark
			// this as a call via a trampoline so that we can potentially
			// switch to a direct call during relocation.
			sb.SetRelocType(ri, objabi.R_RISCV_JAL_TRAMP)
		}
		relocs := sb.Relocs()
		r := relocs.At(ri)
		r.SetSym(tramp)
		r.SetAdd(0)

	case objabi.R_RISCV_CALL:
		// Nothing to do, already using AUIPC+JALR.

	default:
		ctxt.Errorf(s, "trampoline called with non-jump reloc: %d (%s)", r.Type(), sym.RelocName(ctxt.Arch, r.Type()))
	}
}

func genCallTramp(arch *sys.Arch, linkmode ld.LinkMode, ldr *loader.Loader, tramp *loader.SymbolBuilder, target loader.Sym, offset int64) {
	tramp.AddUint32(arch, 0x00000f97) // AUIPC	$0, X31
	tramp.AddUint32(arch, 0x000f8067) // JALR	X0, (X31)

	r, _ := tramp.AddRel(objabi.R_RISCV_CALL)
	r.SetSiz(8)
	r.SetSym(target)
	r.SetAdd(offset)
}
