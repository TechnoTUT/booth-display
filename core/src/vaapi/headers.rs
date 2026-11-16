//! SPS and PPS for the encoder's stream. VA drivers (iHD and i965 alike) write
//! slice headers themselves but emit no parameter sets unless the application
//! packs them, so they are written here, from the same values the encoder
//! passes to the driver, and put in front of every IDR.

/// Bit writer for RBSP data with Exp-Golomb codes.
#[derive(Default)]
struct Bits {
    out: Vec<u8>,
    cur: u8,
    n: u8,
}

impl Bits {
    fn u(&mut self, bits: u32, v: u32) {
        for i in (0..bits).rev() {
            self.cur = self.cur << 1 | (v >> i & 1) as u8;
            self.n += 1;
            if self.n == 8 {
                self.out.push(self.cur);
                self.cur = 0;
                self.n = 0;
            }
        }
    }
    fn flag(&mut self, b: bool) {
        self.u(1, b as u32);
    }
    fn ue(&mut self, v: u32) {
        let x = v as u64 + 1;
        let len = 64 - x.leading_zeros();
        self.u(len - 1, 0);
        self.u(len, x as u32);
    }
    fn se(&mut self, v: i32) {
        self.ue(if v > 0 { 2 * v as u32 - 1 } else { (-2 * v) as u32 });
    }
    /// rbsp_trailing_bits, then the NAL unit with start code and emulation prevention.
    fn nal(mut self, header: u8) -> Vec<u8> {
        self.u(1, 1);
        while self.n != 0 {
            self.u(1, 0);
        }
        let mut nal = vec![0, 0, 0, 1, header];
        let mut zeros = 0;
        for b in self.out {
            if zeros >= 2 && b <= 3 {
                nal.push(3);
                zeros = 0;
            }
            zeros = if b == 0 { zeros + 1 } else { 0 };
            nal.push(b);
        }
        nal
    }
}

pub struct SpsParams {
    pub level_idc: u8,
    pub width_mbs: u32,
    pub height_mbs: u32,
    /// Crop in luma samples (right, bottom); must be even.
    pub crop_right: u32,
    pub crop_bottom: u32,
    pub log2_max_frame_num: u32,
    pub log2_max_poc_lsb: u32,
    pub fps: u32,
}

/// Constrained Baseline SPS with one reference frame, frames only, and a VUI
/// that tells decoders there is no reordering (output each frame at once).
pub fn sps(p: &SpsParams) -> Vec<u8> {
    let mut b = Bits::default();
    b.u(8, 66); // profile_idc: Baseline
    b.u(8, 0b1100_0000); // constraint_set0_flag, constraint_set1_flag: Constrained Baseline
    b.u(8, p.level_idc as u32);
    b.ue(0); // seq_parameter_set_id
    b.ue(p.log2_max_frame_num - 4);
    b.ue(0); // pic_order_cnt_type
    b.ue(p.log2_max_poc_lsb - 4);
    b.ue(1); // max_num_ref_frames
    b.flag(false); // gaps_in_frame_num_value_allowed_flag
    b.ue(p.width_mbs - 1);
    b.ue(p.height_mbs - 1);
    b.flag(true); // frame_mbs_only_flag
    b.flag(true); // direct_8x8_inference_flag
    let crop = p.crop_right > 0 || p.crop_bottom > 0;
    b.flag(crop);
    if crop {
        b.ue(0);
        b.ue(p.crop_right / 2);
        b.ue(0);
        b.ue(p.crop_bottom / 2);
    }
    b.flag(true); // vui_parameters_present_flag
    b.flag(false); // aspect_ratio_info_present_flag
    b.flag(false); // overscan_info_present_flag
    b.flag(false); // video_signal_type_present_flag
    b.flag(false); // chroma_loc_info_present_flag
    b.flag(true); // timing_info_present_flag
    b.u(32, 1); // num_units_in_tick
    b.u(32, p.fps * 2); // time_scale
    b.flag(true); // fixed_frame_rate_flag
    b.flag(false); // nal_hrd_parameters_present_flag
    b.flag(false); // vcl_hrd_parameters_present_flag
    b.flag(false); // pic_struct_present_flag
    b.flag(true); // bitstream_restriction_flag
    b.flag(true); // motion_vectors_over_pic_boundaries_flag
    b.ue(0); // max_bytes_per_pic_denom
    b.ue(0); // max_bits_per_mb_denom
    b.ue(15); // log2_max_mv_length_horizontal
    b.ue(15); // log2_max_mv_length_vertical
    b.ue(0); // max_num_reorder_frames
    b.ue(1); // max_dec_frame_buffering
    b.nal(0x67)
}

/// PPS matching the picture parameters: CAVLC, one slice group, one active
/// reference, the given initial QP, deblocking control in slice headers.
pub fn pps(pic_init_qp: u8) -> Vec<u8> {
    let mut b = Bits::default();
    b.ue(0); // pic_parameter_set_id
    b.ue(0); // seq_parameter_set_id
    b.flag(false); // entropy_coding_mode_flag: CAVLC
    b.flag(false); // bottom_field_pic_order_in_frame_present_flag
    b.ue(0); // num_slice_groups_minus1
    b.ue(0); // num_ref_idx_l0_default_active_minus1
    b.ue(0); // num_ref_idx_l1_default_active_minus1
    b.flag(false); // weighted_pred_flag
    b.u(2, 0); // weighted_bipred_idc
    b.se(pic_init_qp as i32 - 26); // pic_init_qp_minus26
    b.se(0); // pic_init_qs_minus26
    b.se(0); // chroma_qp_index_offset
    b.flag(true); // deblocking_filter_control_present_flag
    b.flag(false); // constrained_intra_pred_flag
    b.flag(false); // redundant_pic_cnt_present_flag
    b.nal(0x68)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn exp_golomb() {
        let mut b = Bits::default();
        for v in [0, 1, 2, 3, 7] {
            b.ue(v);
        }
        b.se(-1);
        b.se(2);
        // 1 010 011 00100 0001000 011 00100 + trailing 1
        let nal = b.nal(0);
        assert_eq!(&nal[5..], &[0b1010_0110, 0b0100_0001, 0b0000_1100, 0b1001_0000]);
    }

    #[test]
    fn emulation_prevention() {
        let mut b = Bits::default();
        b.u(32, 0x0000_0100);
        let nal = b.nal(0x67);
        assert_eq!(&nal[5..], &[0, 0, 3, 1, 0, 0x80]);
    }
}
